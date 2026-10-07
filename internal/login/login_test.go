package login

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/claudeconfig"
)

func TestNewContext(t *testing.T) {
	ctx := NewContext()
	if ctx.Runner == nil {
		t.Fatal("Runner should not be nil")
	}
	if ctx.HomeDir == "" {
		t.Fatal("HomeDir should be set")
	}
}

// The two shapes of coding-CLI login Run supports, as the claude and
// codex definitions in internal/providers use them (those definitions are
// tested there): a login that seeds state afterwards, and one whose
// command needs a scoped environment and must leave a credential behind.
func claudeLike() CLI {
	return CLI{
		Binary: "claude", Args: []string{"auth", "login"},
		Hint: "Claude remote login: if the URL is clipped, press c to copy it; paste the browser's code here when prompted.\n",
		After: func(home string) error {
			if err := claudeconfig.SeedRemoteConsent(home); err != nil {
				return fmt.Errorf("claude auth login succeeded, but seeding Remote Control consent failed: %w", err)
			}
			return nil
		},
	}
}

func codexLike() CLI {
	return CLI{
		Binary: "codex", Args: []string{"login", "--device-auth"},
		Env: func(home string) []string { return []string{"CODEX_HOME=" + filepath.Join(home, ".codex")} },
		After: func(home string) error {
			auth := filepath.Join(home, ".codex", "auth.json")
			if fi, err := os.Stat(auth); err != nil || fi.Size() == 0 {
				return fmt.Errorf("Codex login completed without creating %s", auth)
			}
			return nil
		},
	}
}

// Hermetic equivalent of the reference smoke test
// test_login_claude_uses_auth_login: the login calls exactly the CLI's
// login command.
func TestRun_CallsTheLoginCommand(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	if err := Run(ctx, claudeLike()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := env.callLog(t, "claude")
	if len(calls) != 1 {
		t.Fatalf("claude was invoked %d times, want 1", len(calls))
	}
	if strings.Join(calls[0], " ") != "auth login" {
		t.Fatalf("claude invoked with %v, want [auth login]", calls[0])
	}
}

// A failed login command is reported, and After (here: consent seeding)
// never runs.
func TestRun_PropagatesFailureWithoutAfter(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if err := os.WriteFile(env.StateDir+"/claude-fail", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, claudeLike()); err == nil || !strings.HasPrefix(err.Error(), "claude auth login failed: exit ") {
		t.Fatalf("expected the failed command to be named, got %v", err)
	}
	if _, err := os.Stat(env.Home + "/.claude.json"); !os.IsNotExist(err) {
		t.Fatalf("a failed login still seeded Remote Control consent: %v", err)
	}
}

// Reference login_claude: the login runs attached to the operator's
// terminal (its output reaches the operator directly) after the hint, and
// a successful login runs After (seeding remoteDialogSeen while preserving
// the rest of ~/.claude.json).
func TestRun_AttachesTerminalAndRunsAfter(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if err := os.WriteFile(env.Home+"/.claude.json", []byte(`{"userID":"u1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, claudeLike()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	out := ctx.Stdout.(*strings.Builder).String()
	if !strings.HasPrefix(out, "Claude remote login: if the URL is clipped, press c to copy it;") {
		t.Fatalf("login hint missing: %q", out)
	}
	if !strings.Contains(out, "Logged in to Claude.") {
		t.Fatalf("claude's own output did not reach the operator: %q", out)
	}
	cfg, err := os.ReadFile(env.Home + "/.claude.json")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"userID\": \"u1\",\n  \"remoteDialogSeen\": true\n}\n"
	if string(cfg) != want {
		t.Fatalf("~/.claude.json = %q, want %q", cfg, want)
	}
}

// Hermetic equivalent of the reference smoke test
// test_login_codex_uses_device_auth: the command's environment (CODEX_HOME)
// is scoped to that one child process, never exported to the codvps
// process itself.
func TestRun_ScopesTheCommandEnvironment(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("CODEX_HOME", "") // never set in the test process itself

	if err := Run(ctx, codexLike()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := env.callLog(t, "codex")
	if len(calls) != 1 {
		t.Fatalf("codex was invoked %d times, want 1", len(calls))
	}
	if strings.Join(calls[0], " ") != "login --device-auth" {
		t.Fatalf("codex invoked with %v, want [login --device-auth]", calls[0])
	}
	if _, err := os.Stat(env.Home + "/.codex/auth.json"); err != nil {
		t.Fatalf("codex did not see its scoped CODEX_HOME: %v", err)
	}
	if got := os.Getenv("CODEX_HOME"); got != "" {
		t.Fatalf("CODEX_HOME leaked into the codvps process itself: %q", got)
	}
}

// Reference login_codex: a login that exits 0 but leaves no stored
// credential is still a failure, as After reports it.
func TestRun_AfterCanFailASuccessfulCommand(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if err := os.WriteFile(env.StateDir+"/codex-no-auth", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(ctx, codexLike())
	want := "Codex login completed without creating " + env.Home + "/.codex/auth.json"
	if err == nil || err.Error() != want {
		t.Fatalf("Run() = %v, want %q", err, want)
	}
}

// A device code is shown while the CLI waits, so its output reaches the
// operator's terminal directly rather than after the process exits.
func TestRun_ScopedCommandStaysAttachedToTheTerminal(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if err := Run(ctx, codexLike()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := ctx.Stdout.(*strings.Builder).String(); !strings.Contains(got, "Logged in to Codex.") {
		t.Fatalf("codex output did not reach Stdout: %q", got)
	}
}

func TestRun_ScopedCommandPropagatesFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if err := os.WriteFile(env.StateDir+"/codex-fail", nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(ctx, codexLike()); err == nil {
		t.Fatal("expected an error when codex login fails")
	}
}

// Preflight never fails and never prints anything that looks like a token
// value (only presence/absence and a storage kind).
func TestPreflight_NeverFailsNoSecrets(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	report := Preflight(ctx)
	if !strings.Contains(report, "operator:") {
		t.Fatal("preflight did not report the operator")
	}
	if !strings.Contains(report, "gh account: octocat") {
		t.Fatalf("preflight did not report the gh account:\n%s", report)
	}
	if !strings.Contains(report, "credential storage:") {
		t.Fatal("preflight did not report credential storage")
	}
	for _, secret := range []string{"gho_", "ghp_", "Bearer "} {
		if strings.Contains(report, secret) {
			t.Fatalf("preflight report contains something that looks like a token: %q", secret)
		}
	}
}

func TestPreflight_ReportsMissingTool(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	// Remove the fake gh, and — critically — drop /usr/bin and /bin from
	// PATH entirely so this can never fall through to a real gh (this host
	// has one installed): only the sandboxed bin dir remains, with a
	// symlink to the real bash so the other fakes' #!/usr/bin/env bash
	// shebang still resolves.
	binDir := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
	if err := os.Remove(binDir + "/gh"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/bash", binDir+"/bash"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	if _, err := exec.LookPath("gh"); err == nil {
		t.Fatal("test setup is broken: a gh executable is still resolvable on PATH")
	}

	report := Preflight(ctx)
	if !strings.Contains(report, "missing (install: gh)") {
		t.Fatalf("preflight did not report gh as missing with a package name:\n%s", report)
	}
}

// TestDefaultIsTTY_DevNullIsNotATTY verifies that /dev/null is correctly
// identified as not a TTY, even though it is a character device. This is a
// critical security check: authentication must not be bypassed when stdin
// is a pipe or redirection.
func TestDefaultIsTTY_DevNullIsNotATTY(t *testing.T) {
	nullFile, err := os.Open("/dev/null")
	if err != nil {
		t.Skipf("cannot open /dev/null: %v", err)
	}
	defer func() { _ = nullFile.Close() }()

	// Save current os.Stdin and temporarily redirect it to /dev/null
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	os.Stdin = nullFile

	// defaultIsTTY should return false for /dev/null even though it's a
	// character device. The ioctl-based check should fail on /dev/null
	// because it doesn't support terminal operations (TCGETS ioctl).
	if got := defaultIsTTY(); got {
		t.Fatal("defaultIsTTY() returned true for /dev/null; should be false")
	}
}

// TestDefaultIsTTY_RegularFileIsNotATTY verifies that regular files are
// correctly identified as not TTYs.
func TestDefaultIsTTY_RegularFileIsNotATTY(t *testing.T) {
	f, err := os.CreateTemp("", "tty-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	defer func() { _ = os.Remove(f.Name()) }()

	// Save current os.Stdin and temporarily redirect it to a regular file
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	os.Stdin = f

	if got := defaultIsTTY(); got {
		t.Fatal("defaultIsTTY() returned true for a regular file; should be false")
	}
}
