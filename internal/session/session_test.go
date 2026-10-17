package session

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	if err != nil || claude.Dir != dest || !reflect.DeepEqual(claude.Args, []string{"remote-control"}) {
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
