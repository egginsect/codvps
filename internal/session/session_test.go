package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

func fixture(t *testing.T) (*paths.Layout, string) {
	t.Helper()
	layout, _ := paths.NewForHome(t.TempDir(), "")
	dest := filepath.Join(t.TempDir(), "repo with spaces;literal")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code, err := runner.NewExecRunner().Run("git", "init", dest); err != nil || code != 0 {
		t.Fatalf("git init %v %s", err, stderr)
	}
	if err := registry.WriteEntries(layout.RepositoriesPath(), []registry.Entry{{Name: "sample", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"claude", "codex", "agent"} {
		path := filepath.Join(layout.Home(), ".local", "bin", bin)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return layout, dest
}

func TestInteractiveUsesRegisteredDestinationAndLiteralArgs(t *testing.T) {
	layout, dest := fixture(t)
	if err := settings.Write(layout, settings.Config{RepoRoot: "/unrelated/new-root", GitHubConfigDir: "/auth", GitConfigGlobal: "/auth/gitconfig"}); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"claude", "codex", "cursor"} {
		plan, err := Interactive(layout, provider, "sample", []string{"--help", "$(touch nope); literal"}, runner.NewExecRunner())
		if err != nil {
			t.Fatal(err)
		}
		if plan.Dir != dest || !reflect.DeepEqual(plan.Args, []string{"--help", "$(touch nope); literal"}) {
			t.Fatalf("plan=%+v", plan)
		}
		if !slices.Contains(plan.Env, "GH_CONFIG_DIR=/auth") {
			t.Fatalf("missing shared auth setting: %+v", plan)
		}
		if provider == "codex" && !slices.Contains(plan.Env, "CODEX_HOME="+filepath.Join(layout.Home(), ".codex")) {
			t.Fatal("wrong Codex home")
		}
	}
	if _, err := Interactive(layout, "claude", "unregistered", nil, runner.NewExecRunner()); err == nil {
		t.Fatal("accepted unregistered repo")
	}
}

func TestRemoteRespectsProviderScopes(t *testing.T) {
	layout, dest := fixture(t)
	git := runner.NewExecRunner()
	claude, err := Remote(layout, "run", "claude", "sample", git)
	if err != nil || claude.Dir != dest || !reflect.DeepEqual(claude.Args, claudeRemoteArgs("sample")) {
		t.Fatalf("Claude %+v %v", claude, err)
	}
	cursor, err := Remote(layout, "run", "cursor", "sample", git)
	if err != nil || !slices.Contains(cursor.Args, dest) || !slices.Contains(cursor.Args, filepath.Join(layout.DataDir(), "cursor-workers", "sample")) {
		t.Fatalf("Cursor %+v %v", cursor, err)
	}
	for _, args := range [][]string{{"start", "codex", "sample"}, {"start", "codex", ""}, {"run", "codex", ""}, {"stop", "claude", "sample"}, {"run", "cursor", ""}} {
		if _, err := Remote(layout, args[0], args[1], args[2], git); err == nil {
			t.Fatalf("accepted invalid lifecycle %q", args)
		}
	}
	standalone := head.CodexStandalone(filepath.Join(layout.Home(), ".codex"), "codex")
	if err := os.MkdirAll(filepath.Dir(standalone), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standalone, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "stop", "pair"} {
		plan, err := Remote(layout, action, "codex", "", git)
		if err != nil || plan.Dir != layout.Home() || !reflect.DeepEqual(plan.Args, []string{"remote-control", action, "--json"}) {
			t.Fatalf("Codex %+v %v", plan, err)
		}
		// Only start goes through the unit's bounded-retry helper.
		if plan.BoundedRetry != (action == "start") {
			t.Fatalf("Codex %s BoundedRetry = %v", action, plan.BoundedRetry)
		}
	}
	for _, args := range [][]string{{"pair", "codex", "sample"}, {"stop", "codex", "sample"}, {"status", "codex", ""}, {"run", "opencode", "sample"}} {
		if _, err := Remote(layout, args[0], args[1], args[2], git); err == nil {
			t.Fatalf("accepted invalid lifecycle %q", args)
		}
	}
}

// claudeRemoteArgs is what claude-remote@.service has always run.
func claudeRemoteArgs(repository string) []string {
	return []string{"remote-control", "--spawn", "worktree", "--capacity", "4", "--no-create-session-in-dir", "--remote-control-session-name-prefix", "cloud-" + repository}
}

func installStandalone(t *testing.T, layout *paths.Layout) string {
	t.Helper()
	standalone := head.CodexStandalone(filepath.Join(layout.Home(), ".codex"), "codex")
	if err := os.MkdirAll(filepath.Dir(standalone), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standalone, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return standalone
}

func TestStartIsTheSharedRemoteStart(t *testing.T) {
	layout, dest := fixture(t)
	git := runner.NewExecRunner()
	claude, err := Start(layout, "claude", "sample", git)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Binary != filepath.Join(layout.Home(), ".local", "bin", "claude") || claude.Dir != dest || !reflect.DeepEqual(claude.Args, claudeRemoteArgs("sample")) {
		t.Fatalf("Claude %+v", claude)
	}
	if !reflect.DeepEqual(claude.RemoveEnv, []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}) || claude.BoundedRetry {
		t.Fatalf("Claude environment %+v", claude)
	}
	cursor, err := Start(layout, "cursor", "sample", git)
	if err != nil {
		t.Fatal(err)
	}
	wantCursor := []string{"worker", "--name", "codvps-sample", "--worker-dir", dest, "--data-dir", filepath.Join(layout.DataDir(), "cursor-workers", "sample"), "start"}
	if cursor.Dir != dest || !reflect.DeepEqual(cursor.Args, wantCursor) || !slices.Contains(cursor.Env, "NO_OPEN_BROWSER=1") {
		t.Fatalf("Cursor %+v", cursor)
	}
	for _, name := range []string{"claude", "cursor"} {
		if _, err := Start(layout, name, "", git); err == nil {
			t.Fatalf("%s started without a repository", name)
		}
		if _, err := Start(layout, name, "unregistered", git); err == nil {
			t.Fatalf("%s started an unregistered repository", name)
		}
	}
	if _, err := Start(layout, "codex", "", git); err == nil {
		t.Fatal("Codex started without the standalone install")
	}
	standalone := installStandalone(t, layout)
	codex, err := Start(layout, "codex", "", git)
	if err != nil {
		t.Fatal(err)
	}
	if codex.Binary != standalone || codex.Dir != layout.Home() || !codex.BoundedRetry || !reflect.DeepEqual(codex.Args, []string{"remote-control", "start", "--json"}) {
		t.Fatalf("Codex %+v", codex)
	}
	if !slices.Contains(codex.Env, "CODEX_HOME="+filepath.Join(layout.Home(), ".codex")) {
		t.Fatalf("Codex env %q", codex.Env)
	}
	if _, err := Start(layout, "codex", "sample", git); err == nil || !strings.Contains(err.Error(), "host-wide") {
		t.Fatalf("Codex accepted a repository: %v", err)
	}
	stop, err := Stop(layout, "codex", "")
	if err != nil || stop.BoundedRetry || !reflect.DeepEqual(stop.Args, []string{"remote-control", "stop", "--json"}) || stop.Dir != layout.Home() {
		t.Fatalf("Codex stop %+v %v", stop, err)
	}
	for _, name := range []string{"claude", "cursor"} {
		if _, err := Stop(layout, name, "sample"); err == nil {
			t.Fatalf("%s has no native stop", name)
		}
	}
}

// The units resolve their executable on the login-shell PATH as before:
// claude by name, Cursor's agent at ~/.local/bin/agent. Everything else is
// Start's.
// The cursor unit registers the worker name and data directory it always has,
// so upgrading does not turn existing systemd workers into new ones.
func TestUnitStartKeepsCursorWorkerIdentity(t *testing.T) {
	layout, dest := fixture(t)
	old := hostname
	hostname = func() (string, error) { return "vps1", nil }
	t.Cleanup(func() { hostname = old })
	unit, err := UnitStart(layout, "cursor", "sample", runner.NewExecRunner())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"worker", "--name", "vps1-sample", "--worker-dir", ".", "--data-dir", filepath.Join(layout.Home(), ".local", "share", "cursor-agent", "codvps-workers", "sample"), "start"}
	if !reflect.DeepEqual(unit.Args, want) {
		t.Errorf("cursor unit args = %q, want %q", unit.Args, want)
	}
	if unit.Dir != dest {
		t.Errorf("cursor unit dir = %q, want checkout %q", unit.Dir, dest)
	}
	if !slices.Contains(unit.Env, "NO_OPEN_BROWSER=1") {
		t.Errorf("cursor unit env = %q, want NO_OPEN_BROWSER=1", unit.Env)
	}
}

func TestUnitStartKeepsUnitExecutables(t *testing.T) {
	layout, _ := fixture(t)
	git := runner.NewExecRunner()
	for name, binary := range map[string]string{"claude": "claude", "cursor": filepath.Join(layout.Home(), ".local", "bin", "agent")} {
		unit, err := UnitStart(layout, name, "sample", git)
		if err != nil {
			t.Fatal(err)
		}
		manual, err := Start(layout, name, "sample", git)
		if err != nil {
			t.Fatal(err)
		}
		if unit.Binary != binary {
			t.Errorf("%s unit binary = %q, want %q", name, unit.Binary, binary)
		}
		unit.Binary = manual.Binary
		if name == "cursor" {
			// Units keep Cursor's historical worker identity; see
			// TestUnitStartKeepsCursorWorkerIdentity.
			unit.Args = manual.Args
		}
		if !reflect.DeepEqual(unit, manual) {
			t.Errorf("%s unit plan %+v differs from Start %+v", name, unit, manual)
		}
	}
	if _, err := UnitStart(layout, "codex", "", git); err == nil {
		t.Fatal("UnitStart accepted codex")
	}
	// The unit start needs no resolvable CLI: the login shell finds it.
	if err := os.Remove(filepath.Join(layout.Home(), ".local", "bin", "claude")); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	onlyGit := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(onlyGit, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", onlyGit)
	if _, err := UnitStart(layout, "claude", "sample", git); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(layout, "claude", "sample", git); err == nil {
		t.Fatal("Start accepted a missing claude CLI")
	}
}

func TestBoundedRetryRunsTheUnitHelperFromHome(t *testing.T) {
	layout, _ := fixture(t)
	standalone := installStandalone(t, layout)
	plan, err := Start(layout, "codex", "", runner.NewExecRunner())
	if err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	fake := runner.NewFakeRunner()
	fake.SetResponse(standalone, []string{"remote-control", "start", "--json"}, runner.Response{
		Stderr: "Error: Remote control is enabled on " + host + " but the connection is errored.\n", ExitCode: 1, Err: errors.New("exit 1"),
	})
	var out, diag strings.Builder
	// The fake keeps failing, so the one retry fails too: start, stop, start.
	err = boundedRetry(plan, func(string) string { return "" }, fake, &out, &diag)
	if err == nil || !strings.Contains(err.Error(), "after one retry") {
		t.Fatalf("err = %v", err)
	}
	var verbs []string
	for _, call := range fake.Calls {
		if call.Name != standalone {
			t.Fatalf("ran %q", call.Name)
		}
		verbs = append(verbs, call.Args[1])
	}
	if !reflect.DeepEqual(verbs, []string{"start", "stop", "start"}) {
		t.Fatalf("verbs = %q", verbs)
	}
}

func TestExecuteReplacesEnvironmentAndRestoresCwdOnFailure(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	plan := Plan{Binary: "/bin/native", Dir: dir, Args: []string{"a b", ";literal"}, Env: []string{"GH_CONFIG_DIR=/new"}, RemoveEnv: []string{"ANTHROPIC_API_KEY"}}
	sentinel := errors.New("launch failed")
	err = execute(plan, []string{"GH_CONFIG_DIR=/old", "ANTHROPIC_API_KEY=do-not-pass", "KEEP=yes"}, func(path string, argv, env []string) error {
		cwd, _ := os.Getwd()
		if cwd != dir || path != plan.Binary || !reflect.DeepEqual(argv, []string{"/bin/native", "a b", ";literal"}) {
			t.Fatalf("incorrect launch %q %q %q", cwd, path, argv)
		}
		if !reflect.DeepEqual(env, []string{"KEEP=yes", "GH_CONFIG_DIR=/new"}) {
			t.Fatalf("env=%q", env)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, _ := os.Getwd()
	if after != before {
		t.Fatalf("cwd changed: %q", after)
	}
}

func TestBinaryRejectsImplicitRelativePATHBeforeChdir(t *testing.T) {
	layout, _ := paths.NewForHome(t.TempDir(), "")
	cwd, _ := os.Getwd()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tools"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tools", "claude"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	t.Setenv("PATH", "tools")
	binary, err := Binary(layout, "claude")
	if err == nil || binary != "" {
		t.Fatalf("accepted implicit current-directory binary=%q err=%v", binary, err)
	}
}

func TestStandaloneGuardRejectsExternalSymlinks(t *testing.T) {
	layout, _ := paths.NewForHome(t.TempDir(), "")
	root := filepath.Join(layout.Home(), ".codex", "packages", "standalone")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "current", "codex")
	if err := validateStandalone(layout, binary); err == nil {
		t.Fatal("accepted unrelated runtime symlink")
	}
	if err := os.Remove(filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(root, "v1")
	if err := os.Mkdir(version, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(version, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(version, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := validateStandalone(layout, binary); err != nil {
		t.Fatal(err)
	}
}
