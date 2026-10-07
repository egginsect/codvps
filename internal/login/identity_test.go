package login

import "testing"

// Scenario 24: explicit --identity-name/--identity-email set both fields.
func TestIdentity_ExplicitFlags(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")
	t.Setenv("GH_FAKE_NAME", "Ada Lovelace")
	t.Setenv("GH_FAKE_ID", "42")

	status, _, err := ensureGitIdentity(ctx, "Ada Lovelace", "ada@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	assertGitConfig(t, ctx, "user.name", "Ada Lovelace")
	assertGitConfig(t, ctx, "user.email", "ada@example.com")
}

// Scenario 25: an already-configured identity is never overwritten, even
// with different flags on a later run.
func TestIdentity_NeverOverwritten(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	if _, _, err := ensureGitIdentity(ctx, "Ada Lovelace", "ada@example.com"); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GH_FAKE_LOGIN", "someone-else")
	t.Setenv("GH_FAKE_NAME", "Someone Else")
	t.Setenv("GH_FAKE_ID", "7")
	status, _, err := ensureGitIdentity(ctx, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	assertGitConfig(t, ctx, "user.name", "Ada Lovelace")
	assertGitConfig(t, ctx, "user.email", "ada@example.com")
}

// Scenario 26: no existing identity and no --identity-* flags leaves git
// config completely untouched (pending).
func TestIdentity_NoFlags_NoExisting_Pending(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "testuser")
	t.Setenv("GH_FAKE_NAME", "Test User")
	t.Setenv("GH_FAKE_ID", "999")

	status, _, err := ensureGitIdentity(ctx, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	assertGitConfigUnset(t, ctx, "user.name")
	assertGitConfigUnset(t, ctx, "user.email")
	if got := gitConfigWrites(t, env); got != 0 {
		t.Fatalf("git config wrote %d values despite nothing to set", got)
	}
}

// gitConfigWrites counts "git config --global <key> <value>" calls (a
// write), as opposed to "git config --global --get <key>" (a read).
func gitConfigWrites(t *testing.T, env *testEnv) int {
	t.Helper()
	n := 0
	for _, call := range env.callLog(t, "git") {
		if len(call) == 4 && call[0] == "config" && call[1] == "--global" && call[2] != "--get" {
			n++
		}
	}
	return n
}

// Scenario 27: --identity-name alone, no existing identity, derives a
// users.noreply.github.com email from the gh account's id and login.
func TestIdentity_NameOnly_DerivesNoreplyEmail(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "testuser")
	t.Setenv("GH_FAKE_NAME", "Test User")
	t.Setenv("GH_FAKE_ID", "999")

	status, _, err := ensureGitIdentity(ctx, "Explicit Name", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	assertGitConfig(t, ctx, "user.name", "Explicit Name")
	assertGitConfig(t, ctx, "user.email", "999+testuser@users.noreply.github.com")
}

// Symmetric case: --identity-email alone derives a name from the gh
// account.
func TestIdentity_EmailOnly_DerivesName(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_FAKE_LOGIN", "testuser")
	t.Setenv("GH_FAKE_NAME", "Test User")
	t.Setenv("GH_FAKE_ID", "999")

	status, _, err := ensureGitIdentity(ctx, "", "explicit@example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	assertGitConfig(t, ctx, "user.name", "Test User")
	assertGitConfig(t, ctx, "user.email", "explicit@example.com")
}

func assertGitConfig(t *testing.T, ctx *Context, key, want string) {
	t.Helper()
	got, set, err := gitConfigGet(ctx, key)
	if err != nil {
		t.Fatalf("gitConfigGet(%s): %v", key, err)
	}
	if !set || got != want {
		t.Fatalf("%s = %q (set=%v), want %q", key, got, set, want)
	}
}

func assertGitConfigUnset(t *testing.T, ctx *Context, key string) {
	t.Helper()
	got, set, err := gitConfigGet(ctx, key)
	if err != nil {
		t.Fatalf("gitConfigGet(%s): %v", key, err)
	}
	if set {
		t.Fatalf("%s = %q, want unset", key, got)
	}
}
