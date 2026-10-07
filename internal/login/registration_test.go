package login

import "testing"

func genKey(t *testing.T, env *testEnv, ctx *Context) string {
	t.Helper()
	key, status, _, err := ensureSSHKey(ctx, true, "generate", true)
	if err != nil || status != StatusOK {
		t.Fatalf("seed key generation: status=%q err=%v", status, err)
	}
	return key
}

// Scenarios 6 and 7: first registration calls `gh ssh-key add` exactly
// once, and records the key against the fake GitHub account state.
func TestRegistration_FirstRun_AddsOnce(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	key := genKey(t, env, ctx)

	status, _, err := ensureSSHKeyRegistered(ctx, true, key, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 1 {
		t.Fatalf("gh ssh-key add called %d times, want 1", got)
	}

	registered, err := sshKeyRegistered(ctx, key+".pub")
	if err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("key was not recorded as registered with the fake GitHub account")
	}
}

// Scenario 8: a second registration run does not call `gh ssh-key add`
// again once the key is already registered.
func TestRegistration_SecondRun_NoDuplicateAdd(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	key := genKey(t, env, ctx)

	if _, _, err := ensureSSHKeyRegistered(ctx, true, key, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureSSHKeyRegistered(ctx, true, key, true); err != nil {
		t.Fatal(err)
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 1 {
		t.Fatalf("gh ssh-key add called %d times across two runs, want 1", got)
	}
}

// Scenario 9: non-interactive without --yes leaves registration pending
// and never calls `gh ssh-key add`.
func TestRegistration_NonInteractive_WithoutYes_Pending(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	key := genKey(t, env, ctx)

	status, _, err := ensureSSHKeyRegistered(ctx, true, key, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 0 {
		t.Fatalf("gh ssh-key add was called without --yes")
	}
}

// Scenario 10: `gh ssh-key list` failing stops registration (a hard
// failure), not a silent "treat as unregistered".
func TestRegistration_ListFailure_Stops(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	key := genKey(t, env, ctx)
	env.setSSHKeyListFail()

	status, _, err := ensureSSHKeyRegistered(ctx, true, key, true)
	if err == nil {
		t.Fatal("expected an error when gh ssh-key list fails")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 0 {
		t.Fatalf("gh ssh-key add was called after gh ssh-key list failed")
	}
}

// Scenario 11: a token missing admin:public_key/write:public_key must not
// add the key.
func TestRegistration_MissingScope_NoAdd(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_SCOPES", "repo,read:org")
	key := genKey(t, env, ctx)

	status, detail, err := ensureSSHKeyRegistered(ctx, true, key, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	if detail == "" {
		t.Fatal("expected a detail message naming the missing scope")
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 0 {
		t.Fatalf("gh ssh-key add was called despite a token missing the required scope")
	}
}

// Scenarios 12 and 13: the detection helper reports false before
// registration and true after.
func TestSSHKeyRegistered_DetectionHelper(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	key := genKey(t, env, ctx)

	before, err := sshKeyRegistered(ctx, key+".pub")
	if err != nil {
		t.Fatal(err)
	}
	if before {
		t.Fatal("sshKeyRegistered reported true before the key was registered")
	}

	if _, _, err := ensureSSHKeyRegistered(ctx, true, key, true); err != nil {
		t.Fatal(err)
	}

	after, err := sshKeyRegistered(ctx, key+".pub")
	if err != nil {
		t.Fatal(err)
	}
	if !after {
		t.Fatal("sshKeyRegistered did not detect the already-registered key")
	}
}

// Binding contract: an unknown account (not logged in) must never have a
// key enrolled against it.
func TestRegistration_NoActiveAccount_Refused(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	key := genKey(t, env, ctx) // not logged in

	status, _, err := ensureSSHKeyRegistered(ctx, true, key, true)
	if err == nil {
		t.Fatal("expected an error with no active account")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if got := env.countCalls(t, "gh", "ssh-key", "add"); got != 0 {
		t.Fatal("a key was added for an unknown account")
	}
}
