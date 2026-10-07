package login

import (
	"errors"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

// Scenario 32: --non-interactive exits 2 while logged out.
func TestEnsureAuth_NonInteractive_LoggedOut_Exit2(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()

	status, _, err := ensureAuth(ctx, true)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
}

// Scenario 33: a missing terminal no longer stops the
// device flow (see TestGitHub_NoTTY_LoggedOut_StartsDeviceFlow); only the
// explicit --non-interactive request exits 2 and never calls gh auth login.
func TestEnsureAuth_ExplicitNonInteractive_LoggedOut_Exit2(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context() // IsTTY defaults to false in the test sandbox

	// nonInteractive=false requested, but effective mode collapses to
	// non-interactive because there is no tty — GitHub() computes this;
	// here we call ensureAuth the way GitHub() would after that collapse.
	nonInteractive := false || !ctx.IsTTY()
	status, _, err := ensureAuth(ctx, nonInteractive)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %q, want pending", status)
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 0 {
		t.Fatalf("gh auth login was called while not attached to a tty")
	}
}

// Scenario 34: already logged in succeeds in both modes without calling gh
// auth login again.
func TestEnsureAuth_AlreadyLoggedIn_NoRelogin(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	env.setLoggedIn()

	for _, nonInteractive := range []bool{true, false} {
		status, _, err := ensureAuth(ctx, nonInteractive)
		if err != nil {
			t.Fatalf("nonInteractive=%v: unexpected error: %v", nonInteractive, err)
		}
		if status != StatusOK {
			t.Fatalf("nonInteractive=%v: status = %q, want ok", nonInteractive, status)
		}
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 0 {
		t.Fatalf("gh auth login was called while already logged in")
	}
}

// Binding contract bonus: interactive with a real terminal and logged out
// does call gh auth login, and succeeds once it reports success.
func TestEnsureAuth_Interactive_TTY_LoggedOut_LogsIn(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }

	status, _, err := ensureAuth(ctx, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != StatusOK {
		t.Fatalf("status = %q, want ok", status)
	}
	calls := env.callLog(t, "gh")
	found := false
	for _, c := range calls {
		if len(c) >= 3 && c[0] == "auth" && c[1] == "login" && c[2] == "--hostname" {
			found = true
		}
	}
	if !found {
		t.Fatal("gh auth login --hostname ... was not called for an interactive tty session")
	}
	if got := env.countCalls(t, "gh", "ssh-key"); got != 0 {
		t.Fatal("gh ssh-key was called during auth, unrelated to authentication")
	}
	for _, want := range []string{"--skip-ssh-key", "--web", "--scopes", "admin:public_key"} {
		if !containsArg(calls, want) {
			t.Fatalf("gh auth login was not called with %s: %v", want, calls)
		}
	}
	// The device code must reach the operator while gh waits, not after.
	if out := ctx.Stderr.(*strings.Builder).String(); !strings.Contains(out, "one-time code: ABCD-1234") || !strings.Contains(out, "https://github.com/login/device") {
		t.Fatalf("gh's device code was not streamed to the operator; stderr = %q", out)
	}
}

func containsArg(calls [][]string, want string) bool {
	for _, c := range calls {
		for _, a := range c {
			if a == want {
				return true
			}
		}
	}
	return false
}

// Credential storage is reported without ever reading a token value: an
// env override is reported as "env" even though the fake account also
// reports a tokenSource of its own.
func TestCredentialStorageLabel_EnvOverridesReportedSource(t *testing.T) {
	acct := ghAccount{TokenSource: "keyring"}
	t.Setenv("GH_TOKEN", "would-never-be-read")
	if got := credentialStorageLabel(acct); got != "env" {
		t.Fatalf("credentialStorageLabel = %q, want env", got)
	}
}

func TestCredentialStorageLabel_UnknownWhenUnset(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	acct := ghAccount{TokenSource: "something-gh-invented"}
	if got := credentialStorageLabel(acct); got != "unknown" {
		t.Fatalf("credentialStorageLabel = %q, want unknown", got)
	}
}

// Binding contract: multiple accounts are handled, and the active one is
// selected.
func TestActiveGitHubAccount_MultipleAccounts(t *testing.T) {
	accounts := []ghAccount{
		{Host: "github.com", Login: "old-account", Active: false},
		{Host: "github.com", Login: "current-account", Active: true},
	}
	got, ok := activeGitHubAccount(accounts)
	if !ok {
		t.Fatal("expected an active account")
	}
	if got.Login != "current-account" {
		t.Fatalf("active account = %q, want current-account", got.Login)
	}
}

// TestGhAuthStatusParsesRealGhShape decodes the payload gh 2.101 prints for
// `gh auth status --json hosts`: accounts grouped by host, scopes as one
// comma-separated string, tokenSource as a path, keyring or variable name.
func TestGhAuthStatusParsesRealGhShape(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetResponse("gh", []string{"auth", "status", "--json", "hosts"}, runner.Response{Stdout: `{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"octocat","tokenSource":"/home/user/.config/gh/hosts.yml","scopes":"admin:public_key, gist, read:org, repo","gitProtocol":"https"}]}}`})
	accounts, err := ghAuthStatus(&Context{Runner: fr})
	if err != nil {
		t.Fatal(err)
	}
	account, ok := activeGitHubAccount(accounts)
	if !ok || account.Login != "octocat" {
		t.Fatalf("active account = %+v, %v; accounts %+v", account, ok, accounts)
	}
	if !hasRequiredKeyScope(account) {
		t.Errorf("scopes %q lack admin:public_key", account.Scopes)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	if got := credentialStorageLabel(account); got != "file" {
		t.Errorf("storage label for a hosts.yml path = %q, want file", got)
	}
}

// TestGhAuthStatusLoggedOutIsEmpty: gh prints {"hosts":{}} at exit 0 when
// no account is logged in.
func TestGhAuthStatusLoggedOutIsEmpty(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetResponse("gh", []string{"auth", "status", "--json", "hosts"}, runner.Response{Stdout: `{"hosts":{}}`})
	accounts, err := ghAuthStatus(&Context{Runner: fr})
	if err != nil || len(accounts) != 0 {
		t.Fatalf("accounts = %+v, err = %v; want none", accounts, err)
	}
}

// TestCredentialStorageLabel_RealSources maps gh's tokenSource values.
func TestCredentialStorageLabel_RealSources(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	for src, want := range map[string]string{
		"keyring": "keyring", "GH_TOKEN": "env", "GITHUB_TOKEN": "env",
		"/home/user/.config/gh/hosts.yml": "file", "": "unknown",
	} {
		if got := credentialStorageLabel(ghAccount{TokenSource: src}); got != want {
			t.Errorf("credentialStorageLabel(%q) = %q, want %q", src, got, want)
		}
	}
}

// TestGhAuthStatusTooOldGh names the problem when gh predates
// `auth status --json` (gh 2.45, Ubuntu 24.04's package).
func TestGhAuthStatusTooOldGh(t *testing.T) {
	fr := runner.NewFakeRunner()
	fr.SetResponse("gh", []string{"auth", "status", "--json", "hosts"}, runner.Response{Stderr: "unknown flag: --json\n\nUsage:  gh auth status [flags]\n", ExitCode: 1})
	_, err := ghAuthStatus(&Context{Runner: fr})
	if err == nil || !strings.Contains(err.Error(), "this gh is too old for codvps") {
		t.Fatalf("err = %v; want the too-old gh guidance", err)
	}
}
