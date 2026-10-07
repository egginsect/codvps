package login

import (
	"os"
	"strings"
	"testing"
)

// Scenario 1: ensure_ssh_key non-interactive, no --key: reports pending,
// creates no key file, selects nothing.
func TestSSHKey_NonInteractive_NoChoice_Pending(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	key, status, _, err := ensureSSHKey(ctx, true, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	if key != "" {
		t.Fatalf("selected key = %q, want empty", key)
	}
	if _, statErr := os.Stat(env.keyPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a key file was created with no --key choice given")
	}
}

// Scenario 2: --key generate without --passphrase-empty fails and creates
// no key file.
func TestSSHKey_Generate_NonInteractive_RequiresPassphraseEmpty(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	_, status, _, err := ensureSSHKey(ctx, true, "generate", false)
	if err == nil {
		t.Fatal("expected an error when --key generate is used non-interactively without --passphrase-empty")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if _, statErr := os.Stat(env.keyPath()); !os.IsNotExist(statErr) {
		t.Fatalf("a key file was created despite the missing --passphrase-empty")
	}
}

// Scenarios 3 and 4: --key generate --passphrase-empty creates both files,
// and the private key has mode 0600.
func TestSSHKey_Generate_NonInteractive_CreatesPairMode0600(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	key, status, _, err := ensureSSHKey(ctx, true, "generate", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	if key != env.keyPath() {
		t.Fatalf("selected key = %q, want %q", key, env.keyPath())
	}
	fi, err := os.Stat(env.keyPath())
	if err != nil {
		t.Fatalf("private key was not created: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o, want 0600", fi.Mode().Perm())
	}
	if _, err := os.Stat(env.keyPath() + ".pub"); err != nil {
		t.Fatalf("public key was not created: %v", err)
	}
}

// Scenario 5: a second generate run on an existing key is idempotent: the
// file is untouched (same content) and ssh-keygen is not invoked again.
func TestSSHKey_Generate_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	if _, _, _, err := ensureSSHKey(ctx, true, "generate", true); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	before, err := os.ReadFile(env.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	firstCalls := env.countCalls(t, "ssh-keygen", "-t")

	key, status, _, err := ensureSSHKey(ctx, true, "generate", true)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}
	if status != StatusOK || key != env.keyPath() {
		t.Fatalf("second generate: key=%q status=%q", key, status)
	}
	after, err := os.ReadFile(env.keyPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("existing key was regenerated on a second, idempotent run")
	}
	if got := env.countCalls(t, "ssh-keygen", "-t"); got != firstCalls {
		t.Fatalf("ssh-keygen -t was invoked again on an idempotent run (calls: %d -> %d)", firstCalls, got)
	}
}

// --key skip: no key is selected, and nothing under ~/.ssh is touched.
func TestSSHKey_Skip(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	key, status, _, err := ensureSSHKey(ctx, true, "skip", false)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusSkipped || key != "" {
		t.Fatalf("key=%q status=%q, want skipped/empty", key, status)
	}
	if calls := env.callLog(t, "ssh-keygen"); len(calls) != 0 {
		t.Fatalf("--key skip invoked ssh-keygen: %v", calls)
	}
}

// --key existing:<path> uses the given key when it and its .pub are valid.
func TestSSHKey_Existing_ValidPair(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	custom := env.Home + "/.ssh/my_key"
	if err := os.WriteFile(custom, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(custom+".pub", []byte("ssh-ed25519 AAAA custom\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	key, status, _, err := ensureSSHKey(ctx, true, "existing:"+custom, false)
	if err != nil {
		t.Fatal(err)
	}
	if key != custom || status != StatusOK {
		t.Fatalf("key=%q status=%q, want %q/ok", key, status, custom)
	}
}

// Binding contract: an existing:<path> that is missing must fail.
func TestSSHKey_Existing_Missing(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	_, status, _, err := ensureSSHKey(ctx, true, "existing:"+env.Home+"/.ssh/nope", false)
	if err == nil {
		t.Fatal("expected an error for a missing existing key path")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
}

// Binding contract: an existing:<path> that is a symlink must be refused,
// even when the symlink resolves to a valid regular key file.
func TestSSHKey_Existing_SymlinkRejected(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	real := env.Home + "/.ssh/real_key"
	if err := os.WriteFile(real, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real+".pub", []byte("ssh-ed25519 AAAA real\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := env.Home + "/.ssh/linked_key"
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real+".pub", link+".pub"); err != nil {
		t.Fatal(err)
	}

	_, status, _, err := ensureSSHKey(ctx, true, "existing:"+link, false)
	if err == nil {
		t.Fatal("expected a symlinked existing key path to be rejected")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error %q does not mention the symlink rejection", err)
	}
}

// Binding contract: existing:<path> whose .pub is a symlink is also
// rejected, not just a symlinked private key.
func TestSSHKey_Existing_SymlinkedPubRejected(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	priv := env.Home + "/.ssh/id_priv"
	pub := env.Home + "/.ssh/id_pub_real"
	if err := os.WriteFile(priv, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, []byte("ssh-ed25519 AAAA\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(pub, priv+".pub"); err != nil {
		t.Fatal(err)
	}

	_, status, _, err := ensureSSHKey(ctx, true, "existing:"+priv, false)
	if err == nil {
		t.Fatal("expected a symlinked .pub to be rejected")
	}
	if status != StatusFailed {
		t.Fatalf("status = %q, want failed", status)
	}
}

// Binding contract: interactive generation, attached to a (fake) tty: an
// operator who declines is a skip, not a failure or a generated key.
func TestSSHKey_Interactive_Cancellation(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	ctx.Stdin = strings.NewReader("n\n")

	key, status, _, err := ensureSSHKey(ctx, false, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusSkipped || key != "" {
		t.Fatalf("key=%q status=%q, want skipped/empty", key, status)
	}
	if _, statErr := os.Stat(env.keyPath()); !os.IsNotExist(statErr) {
		t.Fatal("a key was generated despite the operator declining")
	}
}

// Binding contract: interactive generation, explicitly choosing an empty
// passphrase (the *only* case that ever passes -N to ssh-keygen).
func TestSSHKey_Interactive_EmptyPassphrase(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	ctx.Stdin = strings.NewReader("y\ny\n") // generate? yes; empty passphrase? yes

	key, status, _, err := ensureSSHKey(ctx, false, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK || key != env.keyPath() {
		t.Fatalf("key=%q status=%q", key, status)
	}
	calls := env.callLog(t, "ssh-keygen")
	if len(calls) == 0 {
		t.Fatal("ssh-keygen was not invoked")
	}
	if !hasArg(calls[0], "-N") {
		t.Fatalf("ssh-keygen was not called with -N for the explicit empty-passphrase choice: %v", calls[0])
	}
	if got := argValue(calls[0], "-N"); got != "" {
		t.Fatalf("-N = %q, want empty passphrase", got)
	}
}

// CRITICAL, binding contract: interactive generation choosing a *non-empty*
// passphrase must never have codvps read or hold that passphrase. -N must
// be omitted entirely (never passed with the typed value, which would leak
// through /proc/<pid>/cmdline), and ssh-keygen must be run attached to the
// real stdin/stdout/stderr so it prompts on the terminal itself.
func TestSSHKey_Interactive_NonEmptyPassphrase(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	const secretPassphrase = "correct-horse-battery-staple-do-not-log-me"
	// "y" generate, "n" decline the empty-passphrase choice (use a real
	// one), then a line standing in for what the operator would type
	// directly at ssh-keygen's own prompt on the terminal — codvps's own
	// prompting must stop at the "n" and never read this line itself.
	ctx.Stdin = strings.NewReader("y\nn\n" + secretPassphrase + "\n")
	var stdout strings.Builder
	ctx.Stdout = &stdout

	key, status, _, err := ensureSSHKey(ctx, false, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK || key != env.keyPath() {
		t.Fatalf("key=%q status=%q", key, status)
	}
	calls := env.callLog(t, "ssh-keygen")
	if len(calls) == 0 {
		t.Fatal("ssh-keygen was not invoked")
	}
	if hasArg(calls[0], "-N") {
		t.Fatalf("ssh-keygen was called with -N for a non-empty passphrase choice: %v", calls[0])
	}
	// ssh-keygen's progress text landed in stdout, but the caller only
	// ever learns the selected key from the returned path, not by parsing
	// that text.
	if !strings.Contains(stdout.String(), "Generating public/private") {
		t.Fatal("ssh-keygen's own output was not surfaced separately from the key path")
	}
	if key != env.keyPath() {
		t.Fatal("selected key path was derived from something other than the fixed key path")
	}
	assertSecretNeverLogged(t, env, secretPassphrase)
}

// Guard, binding contract: no fixture passphrase or token value ever shows
// up as an argv element in any fake's call log, across a representative
// mix of scenarios (interactive non-empty passphrase, env token, and a
// registered account).
func TestNoSecretsInAnyLoggedArgv(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()
	t.Setenv("GH_TOKEN", "ghp_should_never_appear_in_any_argv")
	t.Setenv("GH_FAKE_LOGIN", "octocat")

	ctx.IsTTY = func() bool { return true }
	const secretPassphrase = "never-let-this-leak-into-argv-or-proc-cmdline"
	ctx.Stdin = strings.NewReader("y\nn\n" + secretPassphrase + "\n")
	if _, _, _, err := ensureSSHKey(ctx, false, "", false); err != nil {
		t.Fatalf("ensureSSHKey: %v", err)
	}

	assertSecretNeverLogged(t, env, "ghp_should_never_appear_in_any_argv")
	assertSecretNeverLogged(t, env, secretPassphrase)
}

func hasArg(call []string, flag string) bool {
	for _, a := range call {
		if a == flag {
			return true
		}
	}
	return false
}

// assertSecretNeverLogged checks every fake's call log for the given
// secret value, failing the test if it appears anywhere.
func assertSecretNeverLogged(t *testing.T, env *testEnv, secret string) {
	t.Helper()
	for _, name := range []string{"gh", "ssh", "ssh-keygen", "git", "claude", "codex"} {
		for _, call := range env.callLog(t, name) {
			for _, arg := range call {
				if arg == secret {
					t.Fatalf("fake %s was called with the passphrase in argv: %v", name, call)
				}
			}
		}
	}
}

// Interactive mode reuses an already-existing key without prompting.
func TestSSHKey_Interactive_ExistingPairReused(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	ctx.Stdin = strings.NewReader("") // no prompt should be read

	if _, _, _, err := ensureSSHKey(ctx, true, "generate", true); err != nil {
		t.Fatalf("seed generate: %v", err)
	}

	key, status, _, err := ensureSSHKey(ctx, false, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK || key != env.keyPath() {
		t.Fatalf("key=%q status=%q", key, status)
	}
}

func argValue(call []string, flag string) string {
	for i, a := range call {
		if a == flag && i+1 < len(call) {
			return call[i+1]
		}
	}
	return ""
}
