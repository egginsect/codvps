package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteVersion(t *testing.T) {
	err := Execute([]string{"version"})
	if err != nil {
		t.Errorf("version command failed: %v", err)
	}
}

func TestExecuteHelp(t *testing.T) {
	err := Execute([]string{"help"})
	if err != nil {
		t.Errorf("help command failed: %v", err)
	}
}

func TestExecuteNoArgs(t *testing.T) {
	err := Execute([]string{})
	if err != nil {
		t.Errorf("no args should show help, got error: %v", err)
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	err := Execute([]string{"unknown-command"})
	if err == nil {
		t.Errorf("unknown command should return an error")
	}
}

func TestHeritageBecomeUnknown(t *testing.T) {
	// heritage is no longer a command - should be unknown
	err := Execute([]string{"heritage", "sync"})
	if err == nil {
		t.Errorf("heritage should be unknown command")
	}
}

func TestLoginWithoutSubcommand(t *testing.T) {
	err := Execute([]string{"login"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("login without subcommand should return InvalidUsageError, got %T", err)
	}
}

// login claude/codex/github are implemented, not stubs: they were
// covered here by TestLoginClaudeNoArgs/TestLoginCodexNoArgs/
// TestLoginGithubNoArgs/TestLoginGithubWithNonInteractive asserting
// NotImplementedError. Calling Execute with bare "login claude"/"codex"/
// "github" now reaches login.NewContext(), which uses the real Runner and
// real stdin/stdout — invoking the real claude/codex/gh binaries (or
// hanging waiting on a prompt) is exactly what this suite must never do.
// The full non-stub behavior is covered hermetically, against fake
// executables and a temp HOME, by internal/login's own tests; this layer
// only needs the flag/argument handling that returns before ever calling
// into internal/login, which the tests below and TestLoginClaudeWithExtraArgs
// exercise.
func TestLoginGithubHelp(t *testing.T) {
	err := Execute([]string{"login", "github", "--help"})
	if err != nil {
		t.Errorf("login github --help should not error, got %v", err)
	}
}

func TestLoginGithubTooManyArgs(t *testing.T) {
	err := Execute([]string{"login", "github", "unexpected-positional-arg"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("login github with an unexpected positional arg should return an InvalidUsageError, got %T", err)
	}
}

func TestLoginClaudeWithExtraArgs(t *testing.T) {
	err := Execute([]string{"login", "claude", "extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("login claude with extra args should return InvalidUsageError, got %T", err)
	}
}

// These repo dispatch tests deliberately clear HOME so that the production
// wiring (a real ExecRunner talking to git/systemctl) fails at
// paths.New(""), before it ever reaches a subprocess: this dispatcher test
// only needs to prove routing (no longer NotImplementedError), and it must
// never shell out to systemctl or git against this host's real state. The
// business logic itself (git operations, head reconciliation, fail-closed
// revocation) is covered hermetically in internal/repo with a FakeRunner
// and a fake HeadReconciler.
func TestRepoAdd(t *testing.T) {
	t.Setenv("HOME", "")
	err := Execute([]string{"repo", "add", "https://example.com"})
	if err == nil {
		t.Errorf("repo add should fail (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("repo add should no longer be not implemented, got %T", err)
	}
}

func TestRepoAddNoUrl(t *testing.T) {
	err := Execute([]string{"repo", "add"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("repo add without url should return InvalidUsageError, got %T", err)
	}
}

func TestRepoList(t *testing.T) {
	t.Setenv("HOME", "")
	err := Execute([]string{"repo", "list"})
	if err == nil {
		t.Errorf("repo list should fail (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("repo list should no longer be not implemented, got %T", err)
	}
}

func TestRepoRemove(t *testing.T) {
	t.Setenv("HOME", "")
	err := Execute([]string{"repo", "remove", "myrepo"})
	if err == nil {
		t.Errorf("repo remove should fail (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("repo remove should no longer be not implemented, got %T", err)
	}
}

func TestHeadEnable(t *testing.T) {
	// HOME unset: the command is dispatched to its implementation, which
	// fails resolving paths before any subprocess (systemctl, sudo, ss,
	// codex) could run.
	t.Setenv("HOME", "")
	err := Execute([]string{"head", "enable", "codex"})
	if err == nil {
		t.Fatalf("head enable codex expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("head enable codex should no longer be not implemented")
	}
}

func TestHeadEnableInvalidTarget(t *testing.T) {
	err := Execute([]string{"head", "enable", "invalid"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("head enable with invalid target should return InvalidUsageError, got %T", err)
	}
}

func TestHeadPair(t *testing.T) {
	// HOME unset: the command is dispatched to its implementation, which
	// fails resolving paths before any subprocess (systemctl, sudo, ss,
	// codex) could run.
	t.Setenv("HOME", "")
	err := Execute([]string{"head", "pair", "codex"})
	if err == nil {
		t.Fatalf("head pair codex expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("head pair codex should no longer be not implemented")
	}
}

func TestHeadPairWrongTarget(t *testing.T) {
	err := Execute([]string{"head", "pair", "claude"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("head pair with wrong target should return InvalidUsageError, got %T", err)
	}
}

func TestHeadEnsure(t *testing.T) {
	// HOME unset: the command is dispatched to its implementation, which
	// fails resolving paths before any subprocess (systemctl, sudo, ss,
	// codex) could run.
	t.Setenv("HOME", "")
	err := Execute([]string{"head", "ensure", "codex"})
	if err == nil {
		t.Fatalf("head ensure codex expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("head ensure codex should no longer be not implemented")
	}
}

// TestConfigUnlinkWithNothingLinked is a successful no-op.
func TestConfigUnlinkWithNothingLinked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := Execute([]string{"config", "unlink"}); err != nil {
		t.Errorf("config unlink with nothing linked = %v, want success", err)
	}
}

// TestConfigLinkRefusesUnregisteredName: a bare name must be a registered
// repository, not just any directory under HOME; nothing is linked.
func TestConfigLinkRefusesUnregisteredName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "stray", "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "stray", "claude", "x.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Execute([]string{"config", "link", "stray"})
	if err == nil || !strings.Contains(err.Error(), "not a registered repository") {
		t.Fatalf("config link stray = %v, want not-registered error", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", "x.md")); err == nil {
		t.Fatal("an unregistered directory was linked")
	}
}

// status is implemented; HOME is cleared so this fails at paths.New("")
// before any real systemctl/loginctl probe (see internal/head for the
// hermetic report tests).
func TestStatus(t *testing.T) {
	t.Setenv("HOME", "")
	err := Execute([]string{"status"})
	if err == nil {
		t.Fatalf("status expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("status should no longer be not implemented")
	}
}

func TestStatusWithArgs(t *testing.T) {
	err := Execute([]string{"status", "extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("status with args should return InvalidUsageError, got %T", err)
	}
}

// doctor is implemented; HOME is cleared so it cannot build its layout and
// exits 2 ("could not run its checks") before any real systemctl/loginctl
// probe (internal/doctor has the hermetic report tests).
func TestDoctor(t *testing.T) {
	t.Setenv("HOME", "")
	err := Execute([]string{"doctor"})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("doctor without HOME should exit 2, got %T %v", err, err)
	}
}

func TestDoctorWithArgs(t *testing.T) {
	err := Execute([]string{"doctor", "extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("doctor with args should return InvalidUsageError, got %T", err)
	}
}

// install and uninstall refuse a non-root caller before touching anything.
func TestInstallAndUninstallRequireRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("must not run install or uninstall as root in tests")
	}
	for _, cmd := range []string{"install", "uninstall"} {
		err := Execute([]string{cmd})
		if err == nil || !strings.Contains(err.Error(), "must be run as root") {
			t.Errorf("%s as non-root: got %v", cmd, err)
		}
	}
	if _, ok := Execute([]string{"uninstall", "--force"}).(*InvalidUsageError); !ok {
		t.Error("uninstall with an unknown flag should be invalid usage")
	}
}

// update is implemented. HOME is cleared so each fails
// building its layout, before any systemctl, vendor CLI or /etc read.
func unstubbedRuntimeCommand(t *testing.T, args ...string) {
	t.Helper()
	t.Setenv("HOME", "")
	err := Execute(args)
	if err == nil {
		t.Fatalf("%v without HOME should fail", args)
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Fatalf("%v should be implemented, got NotImplementedError", args)
	}
	if !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("%v failed for another reason than the missing HOME: %v", args, err)
	}
}

// runtime was the pinned-runtime command; it is gone.
func TestRuntimeCommandRemoved(t *testing.T) {
	if _, ok := Execute([]string{"runtime", "path", "codex"}).(*unknownCommandError); !ok {
		t.Error("runtime should be an unknown command")
	}
}

func TestComponentsList(t *testing.T) {
	err := Execute([]string{"components", "list"})
	// Components list is now implemented and succeeds even with missing registry
	if err != nil {
		t.Errorf("components list should succeed (missing registry is ok), got %v", err)
	}
}

func TestUpdate(t *testing.T) { unstubbedRuntimeCommand(t, "update") }
