package login

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // building a test fixture in the OpenSSH hashed-known_hosts format, not for security
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func knownHostsPath(env *testEnv) string {
	return env.Home + "/.ssh/known_hosts"
}

// Scenarios 14 and 15: the first run adds a github.com entry and calls
// `gh api meta`.
func TestKnownHosts_FirstRun_AddsEntry(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	status, _, err := ensureKnownHosts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	content, err := os.ReadFile(knownHostsPath(env))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "github.com ") {
		t.Fatal("known_hosts is missing a github.com entry after the first run")
	}
	if got := env.countCalls(t, "gh", "api", "meta"); got < 1 {
		t.Fatalf("expected at least one 'gh api meta' call, got %d", got)
	}
}

// Scenario 16: a second run does not grow known_hosts.
func TestKnownHosts_SecondRun_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	if _, _, err := ensureKnownHosts(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(knownHostsPath(env))
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := ensureKnownHosts(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(knownHostsPath(env))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("known_hosts grew on a second, already-populated run:\n%q\n->\n%q", first, second)
	}
}

// Scenarios 17 and 18: gh api meta unavailable stops with no ssh-keyscan
// fallback (there is no ssh-keyscan on the sandboxed PATH at all, so any
// attempt to use it would fail the whole test with "executable file not
// found"), and known_hosts is left untouched.
func TestKnownHosts_MetaUnavailable_StopsAndUntouched(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setMetaFail()

	status, _, err := ensureKnownHosts(ctx)
	if err == nil {
		t.Fatal("expected an error when gh api meta is unavailable")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if fi, statErr := os.Stat(knownHostsPath(env)); statErr == nil && fi.Size() > 0 {
		t.Fatal("known_hosts was written despite gh api meta failing")
	}
}

// Scenario 19: pre-existing hashed entries covering both published keys
// are recognized and left untouched (not rewritten, no growth).
func TestKnownHosts_HashedEntriesRecognized(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	hashed := hashedLine(t, "github.com", "ssh-ed25519 AAAAFAKEEDKEYDATA") + "\n" +
		hashedLine(t, "github.com", "ssh-rsa AAAAFAKERSAKEYDATA") + "\n"
	if err := os.WriteFile(knownHostsPath(env), []byte(hashed), 0o600); err != nil {
		t.Fatal(err)
	}

	status, _, err := ensureKnownHosts(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	after, err := os.ReadFile(knownHostsPath(env))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != hashed {
		t.Fatalf("ensureKnownHosts rewrote already-present hashed entries:\nbefore: %q\nafter:  %q", hashed, after)
	}
}

// Scenario 20: a conflicting github.com entry (a key GitHub does not
// publish) is refused, and the file is left byte-for-byte unchanged.
func TestKnownHosts_ConflictingEntry_Refused(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	before := "github.com ssh-rsa AAAABOGUSDATANOTPUBLISHED\n"
	if err := os.WriteFile(knownHostsPath(env), []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	status, _, err := ensureKnownHosts(ctx)
	if err == nil {
		t.Fatal("expected an error for a conflicting github.com known_hosts entry")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	after, err := os.ReadFile(knownHostsPath(env))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatalf("ensureKnownHosts modified known_hosts despite a conflicting entry:\nbefore: %q\nafter:  %q", before, after)
	}
}

// hashedLine builds one OpenSSH HashKnownHosts-format line for host/keyLine
// ("<keytype> <data>"), independently of the production hashedHostMatches
// code, as a test fixture.
func hashedLine(t *testing.T, host, keyLine string) string {
	t.Helper()
	salt := sha256.Sum256([]byte(host)) // deterministic salt is fine for a fixture
	salt16 := salt[:16]
	mac := hmac.New(sha1.New, salt16)
	mac.Write([]byte(host))
	sum := mac.Sum(nil)
	return "|1|" + base64.StdEncoding.EncodeToString(salt16) + "|" + base64.StdEncoding.EncodeToString(sum) + " " + keyLine
}
