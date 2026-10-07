package login

import (
	"strings"
	"testing"
)

// Scenario 28: verify succeeds against a successful ssh -T.
func TestVerify_Success(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	status, _, err := verify(ctx, "")
	if err != nil {
		t.Fatalf("verify failed against a successful ssh -T: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
}

// Scenario 29: verify fails when ssh -T reports a publickey error.
func TestVerify_PublickeyFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	env.setSSHFail()

	status, _, err := verify(ctx, "")
	if err == nil {
		t.Fatal("verify did not fail when ssh -T reported a publickey error")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
}

// Scenario 30: an SSH login that does not match the gh CLI account fails.
func TestVerify_AccountMismatch(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	t.Setenv("GH_FAKE_SSH_LOGIN", "impostor")

	status, _, err := verify(ctx, "")
	if err == nil {
		t.Fatal("verify did not fail on an SSH/gh account mismatch")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
}

// Scenario 31: with a selected key and no ssh config block routing
// github.com, verify pins the connection to that key.
func TestVerify_PinsSelectedKey(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	key := genKey(t, env, ctx)

	status, _, err := verify(ctx, key)
	if err != nil {
		t.Fatalf("verify failed with a selected key and no ssh config block: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	if got := env.countCalls(t, "ssh", "-i", key); got != 1 {
		t.Fatalf("verify did not pin ssh to the selected key with -i/-o IdentitiesOnly=yes (calls with -i %s: %d)", key, got)
	}
}

// HIGH, binding contract: real OpenSSH writes the "Hi <login>! ..." success
// banner to stderr, not stdout (ssh -T's whole output is a login banner,
// which OpenSSH treats like any other pre-auth server message). This pins
// the fixture itself (banner on stderr, stdout empty, exit 1) and then
// confirms verify treats that combination as success.
func TestVerify_BannerOnStderr_Success(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	stdout, stderr, exitCode, runErr := ctx.Runner.Run("ssh", "-T", "git@github.com")
	if runErr == nil || exitCode != 1 {
		t.Fatalf("fixture: exitCode=%d err=%v, want exit 1 (real OpenSSH's own success exit code)", exitCode, runErr)
	}
	if stdout != "" {
		t.Fatalf("fixture: banner unexpectedly on stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "Hi octocat!") {
		t.Fatalf("fixture: banner missing from stderr: %q", stderr)
	}

	status, _, err := verify(ctx, "")
	if err != nil {
		t.Fatalf("verify did not treat a stderr banner with exit 1 as success: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
}

// A stderr banner naming a different login than the gh account still fails,
// exactly like the stdout case did.
func TestVerify_BannerOnStderr_DifferentLogin_Fails(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	t.Setenv("GH_FAKE_SSH_LOGIN", "someone-else")

	_, stderr, _, _ := ctx.Runner.Run("ssh", "-T", "git@github.com")
	if !strings.Contains(stderr, "Hi someone-else!") {
		t.Fatalf("fixture: expected the differing login's banner on stderr: %q", stderr)
	}

	status, _, err := verify(ctx, "")
	if err == nil {
		t.Fatal("verify accepted a stderr banner naming a different login than the gh account")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
}

// Once ensureSSHConfig has routed github.com to the selected key, verify
// must not add a redundant -i pin.
func TestVerify_NoRedundantPin_WhenConfigRoutes(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	key := genKey(t, env, ctx)
	if _, _, err := ensureSSHConfig(ctx, key); err != nil {
		t.Fatal(err)
	}

	status, _, err := verify(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	if got := env.countCalls(t, "ssh", "-i", key); got != 0 {
		t.Fatalf("verify pinned -i despite ssh config already routing to the selected key")
	}
}
