package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

func manualLayout(t *testing.T) *paths.Layout {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"} {
		t.Setenv(key, "")
	}
	layout, err := paths.New("")
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Write(layout, settings.Config{SessionMode: "manual"}); err != nil {
		t.Fatal(err)
	}
	return layout
}

func TestConfigSettingsCommands(t *testing.T) {
	layout := manualLayout(t)
	cmd := &configCmd{}
	if err := cmd.Execute([]string{"set", "repo_root", "/workspace/shared/repos"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := settings.Read(layout)
	if err != nil || cfg.RepoRoot != "/workspace/shared/repos" || cfg.SessionMode != "manual" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	if err := cmd.Execute([]string{"get", "repo_root"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"set", "repo_root", "relative"}, {"set", "unknown", "value"}, {"set", "session_mode", "invalid"}, {"set", "repo_root"}} {
		if err := cmd.Execute(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	if err := cmd.Execute([]string{"unset", "repo_root"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = settings.Read(layout)
	if err != nil || cfg.RepoRoot != "" || cfg.SessionMode != "manual" {
		t.Fatalf("unset=%+v %v", cfg, err)
	}
}

func TestManualRepoManagerNeverInvokesSystemd(t *testing.T) {
	layout := manualLayout(t)
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "called")
	for _, name := range []string{"systemctl", "sudo", "journalctl", "loginctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 91\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	source := filepath.Join(t.TempDir(), "sample")
	if _, stderr, code, err := runner.NewExecRunner().Run("git", "init", "--bare", source); err != nil || code != 0 {
		t.Fatalf("init: %v %s", err, stderr)
	}
	manager, err := newRepoManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.AddAt(source, filepath.Join(t.TempDir(), "actual checkout")); err != nil {
		t.Fatal(err)
	}
	repos, err := manager.List()
	if err != nil || len(repos) != 1 {
		t.Fatalf("list=%v err=%v", repos, err)
	}
	if _, err := manager.Remove("sample"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("invoked service management in manual mode")
	}
	if err := requireSystemdMode(); err == nil {
		t.Fatal("allowed systemd mode action")
	}
	if err := requireManualMode(layout); err != nil {
		t.Fatal(err)
	}
}

func TestShellExecRegisteredDirectoryAndSharedAuth(t *testing.T) {
	layout := manualLayout(t)
	if err := settings.Write(layout, settings.Config{GitHubConfigDir: "/shared/auth", GitConfigGlobal: "/shared/auth/gitconfig"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "checkout with spaces")
	if _, stderr, code, err := runner.NewExecRunner().Run("git", "init", dest); err != nil || code != 0 {
		t.Fatalf("init: %v %s", err, stderr)
	}
	if err := registry.WriteEntries(layout.RepositoriesPath(), []registry.Entry{{Name: "sample", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	opts, _, env := shellExecOptions(t)
	replace := opts.Exec
	sawCwd := ""
	opts.Exec = func(path string, args, vars []string) error { sawCwd, _ = os.Getwd(); return replace(path, args, vars) }
	before, _ := os.Getwd()
	if err := internalShellExec([]string{"--provider", "claude", "--repo", "sample", "--", "/bin/echo", "ok"}, providers.All(), opts); err != nil {
		t.Fatal(err)
	}
	if sawCwd != dest {
		t.Fatalf("cwd=%q want=%q", sawCwd, dest)
	}
	after, _ := os.Getwd()
	if after != before {
		t.Fatal("failed to restore cwd")
	}
	for _, want := range []string{"GH_CONFIG_DIR=/shared/auth", "GIT_CONFIG_GLOBAL=/shared/auth/gitconfig"} {
		if !slices.Contains(*env, want) {
			t.Fatalf("missing %s from %q", want, *env)
		}
	}
	if err := internalShellExec([]string{"--repo", "missing", "--", "/bin/echo"}, providers.All(), opts); err == nil || !strings.Contains(err.Error(), "registered") {
		t.Fatalf("missing repo error=%v", err)
	}
}

func TestSessionValidationUsesConfiguredGitEnvironment(t *testing.T) {
	layout := manualLayout(t)
	dest := filepath.Join(t.TempDir(), "checkout")
	if _, stderr, code, err := runner.NewExecRunner().Run("git", "init", dest); err != nil || code != 0 {
		t.Fatalf("init: %v %s", err, stderr)
	}
	bad := filepath.Join(t.TempDir(), "bad-gitconfig")
	good := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(bad, []byte("[invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", bad)
	if err := settings.Write(layout, settings.Config{GitConfigGlobal: good, SessionMode: "manual"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.IsPrimaryGitCheckout(runner.NewExecRunner(), dest); err == nil {
		t.Fatal("fixture did not reject invalid default Git config")
	}
	git, err := sessionGit(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.IsPrimaryGitCheckout(git, dest); err != nil {
		t.Fatal(err)
	}
	if err := registry.WriteEntries(layout.RepositoriesPath(), []registry.Entry{{Name: "sample", Path: dest}}); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := shellExecOptions(t)
	if err := internalShellExec([]string{"--provider", "claude", "--repo", "sample", "--", "/bin/echo"}, providers.All(), opts); err != nil {
		t.Fatal(err)
	}
}

func TestManualLoginRetainsNativeConsentWithoutProvisioning(t *testing.T) {
	layout := manualLayout(t)
	binary := filepath.Join(layout.Home(), ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(layout.Home(), "invoked")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + record + "'\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := handleLoginProvider(providers.All().Lookup("claude"), nil); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(record)
	if err != nil || string(args) != "auth\nlogin\n" {
		t.Fatalf("args=%q err=%v", args, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Home(), ".claude.json")); !os.IsNotExist(err) {
		t.Fatal("manual login seeded remote consent/trust")
	}
	if _, err := os.Stat(filepath.Join(layout.Home(), ".claude")); !os.IsNotExist(err) {
		t.Fatal("manual login created provider state beyond native command")
	}
}
