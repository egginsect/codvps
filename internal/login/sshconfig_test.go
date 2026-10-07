package login

import (
	"os"
	"strings"
	"testing"
)

// Scenario 21: without a dedicated key, ~/.ssh/config is never created.
func TestSSHConfig_NoKey_NotCreated(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	status, _, err := ensureSSHConfig(ctx, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusSkipped {
		t.Fatalf("status = %q, want skipped", status)
	}
	if _, statErr := os.Stat(env.Home + "/.ssh/config"); !os.IsNotExist(statErr) {
		t.Fatal("ssh config was created without a dedicated key")
	}
}

// Scenarios 22 and 23: with a key, a Host github.com block with an
// IdentityFile pointing at it is added.
func TestSSHConfig_WithKey_AddsHostBlock(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	key := genKey(t, env, ctx)

	status, _, err := ensureSSHConfig(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	content, err := os.ReadFile(env.Home + "/.ssh/config")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "Host github.com") {
		t.Fatal("ssh config missing Host github.com entry")
	}
	if !strings.Contains(string(content), "IdentityFile "+key) {
		t.Fatal("ssh config missing IdentityFile for the codvps key")
	}
}

// Idempotent: a second call with an existing block does not duplicate it,
// and never adds an insteadOf directive.
func TestSSHConfig_Idempotent_NoInsteadOf(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	key := genKey(t, env, ctx)

	if _, _, err := ensureSSHConfig(ctx, key); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(env.Home + "/.ssh/config")
	if err != nil {
		t.Fatal(err)
	}

	status, _, err := ensureSSHConfig(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	second, err := os.ReadFile(env.Home + "/.ssh/config")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("ssh config was rewritten/duplicated on a second, idempotent call")
	}
	if strings.Contains(string(second), "insteadOf") {
		t.Fatal("ssh config contains an insteadOf directive, which the binding contract forbids")
	}
}

// Binding contract: ssh -G github.com is used to verify the effective key,
// not merely that some Host github.com block exists.
func TestSSHConfig_EffectiveIdentityMatchesSelectedKey(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	key := genKey(t, env, ctx)

	if _, _, err := ensureSSHConfig(ctx, key); err != nil {
		t.Fatal(err)
	}

	got, err := effectiveIdentityFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != key {
		t.Fatalf("ssh -G github.com reports IdentityFile %q, want %q", got, key)
	}
}
