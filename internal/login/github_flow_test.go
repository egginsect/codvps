package login

import (
	"os"
	"strings"
	"testing"
)

// Scenarios 35 and 36: the CLI entry point (GitHub) with --non-interactive,
// logged out, exits 2 before any key, identity or known_hosts change, and
// never calls `gh auth login`.
func TestGitHub_CLIEntryPoint_NonInteractive_LoggedOut_Exit2(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	code, err := GitHub(ctx, true, "", false, "", "", false, false)
	if err == nil {
		t.Fatal("expected an error while logged out")
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}

	if _, statErr := os.Stat(env.keyPath()); !os.IsNotExist(statErr) {
		t.Fatal("a key was generated before authentication succeeded")
	}
	if _, statErr := os.Stat(env.Home + "/.ssh/known_hosts"); !os.IsNotExist(statErr) {
		t.Fatal("known_hosts was written before authentication succeeded")
	}
	if _, statErr := os.Stat(env.Home + "/.ssh/config"); !os.IsNotExist(statErr) {
		t.Fatal("ssh config was written before authentication succeeded")
	}
	if got := gitConfigWrites(t, env); got != 0 {
		t.Fatal("git identity was written before authentication succeeded")
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 0 {
		t.Fatal("gh auth login was called while logged out non-interactively")
	}
}

// --defer runs the read-only preflight only, exits 0, and makes no change
// at all — not even a gh auth status call is required to succeed.
func TestGitHub_Defer_PreflightOnly(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	code, err := GitHub(ctx, false, "", false, "", "", false, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 0 {
		t.Fatal("--defer made a change")
	}
	stdout, _ := ctx.Stdout.(*strings.Builder)
	if !strings.Contains(stdout.String(), "Preflight:") {
		t.Fatal("--defer did not print the preflight report")
	}
}

// A fully non-interactive run that supplies every required flag reaches
// the summary and exits 0. This doubles as the "no-private-bootstrap"
// regression (see TestGitHub_NoPrivateCloneRegression): a deterministic
// successful-auth fixture that completes the whole flow.
func TestGitHub_NonInteractive_FullySpecified_Succeeds(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	code, err := GitHub(ctx, true, "generate", true, "Ada Lovelace", "ada@example.com", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	stdout, _ := ctx.Stdout.(*strings.Builder)
	if !strings.Contains(stdout.String(), "GitHub setup complete.") {
		t.Fatalf("summary did not report completion:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "GitHub setup summary:") {
		t.Fatal("the flow did not reach the summary table")
	}
}

// The no-private-bootstrap regression: a deterministic
// successful-auth fixture must drive the whole flow to its end without
// ever invoking `git clone`. The fake git in this package's sandbox exits
// 99 and logs the attempt if it is ever asked to clone anything, so this
// test would fail loudly (not silently pass) if a clone were reintroduced.
func TestGitHub_NoPrivateCloneRegression(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	code, err := GitHub(ctx, true, "generate", true, "Ada Lovelace", "ada@example.com", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (flow did not reach a clean end)", code)
	}
	stdout, _ := ctx.Stdout.(*strings.Builder)
	if !strings.Contains(stdout.String(), "GitHub setup summary:") {
		t.Fatal("the flow did not reach the summary")
	}
	if calls := env.callLog(t, "git"); len(calls) > 0 {
		for _, c := range calls {
			for _, a := range c {
				if a == "clone" {
					t.Fatalf("git clone was invoked during login github: %v", c)
				}
			}
		}
	}
}

// A pending, non-fatal step (no --key choice) still reaches the summary
// non-interactively, and the overall exit code is 2 (incomplete), not 0 or
// 1 — a pending step is not a hard failure.
func TestGitHub_NonInteractive_PendingKey_Exit2AtSummary(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	code, err := GitHub(ctx, true, "", false, "Ada Lovelace", "ada@example.com", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	stdout, _ := ctx.Stdout.(*strings.Builder)
	if !strings.Contains(stdout.String(), "GitHub setup incomplete") {
		t.Fatalf("summary did not report the incomplete state:\n%s", stdout.String())
	}
	// A pending key step must not be followed by a failing SSH check.
	if got := env.countCalls(t, "ssh", "-T"); got != 0 {
		t.Fatal("verify ran ssh -T despite the key step being pending")
	}
}

// --key skip explicitly skips the key entirely; the run can still reach 0
// (no pending state) when identity is also resolved.
func TestGitHub_NonInteractive_KeySkip_Succeeds(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	code, err := GitHub(ctx, true, "skip", false, "Ada Lovelace", "ada@example.com", false, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got := env.countCalls(t, "ssh", "-T"); got != 0 {
		t.Fatal("verify ran ssh -T despite the key step being skipped")
	}
}

// A hard failure (gh ssh-key list fails during registration) aborts the
// whole run with exit 1 before printing the summary table.
func TestGitHub_HardFailure_Exit1_NoSummary(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	env.setSSHKeyListFail()

	code, err := GitHub(ctx, true, "generate", true, "", "", true, false)
	if err == nil {
		t.Fatal("expected an error")
	}
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	stdout, _ := ctx.Stdout.(*strings.Builder)
	if strings.Contains(stdout.String(), "GitHub setup summary:") {
		t.Fatal("the summary table was printed despite a hard failure")
	}
}
