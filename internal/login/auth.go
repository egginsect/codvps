package login

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/ghcli"
)

// ghAccount is one entry of the `gh auth status --json` contract this
// package defines between codvps and the gh CLI: an array of accounts, each
// carrying the fields codvps needs to drive the rest of the flow without
// ever reading a token value. tokenSource is one of "file", "keyring",
// "env" or "" (unknown); scopes lists the OAuth scopes granted to the
// account's token.
type ghAccount struct {
	Host        string   `json:"host"`
	Login       string   `json:"login"`
	Active      bool     `json:"active"`
	Scopes      ghScopes `json:"scopes"`
	TokenSource string   `json:"tokenSource"`
}

// ghScopes is an account's token scopes. gh reports them as one
// comma-separated string ("admin:public_key, repo").
type ghScopes []string

func (s *ghScopes) UnmarshalJSON(data []byte) error {
	var joined string
	if err := json.Unmarshal(data, &joined); err != nil {
		return fmt.Errorf("gh scopes are not a string: %w", err)
	}
	*s = nil
	for _, scope := range strings.Split(joined, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			*s = append(*s, scope)
		}
	}
	return nil
}

// ghAuthStatusOutput is `gh auth status --json hosts`: every known host
// with its accounts, e.g. {"hosts":{"github.com":[{...}]}}; logged out is
// {"hosts":{}} at exit 0.
type ghAuthStatusOutput struct {
	Hosts map[string][]ghAccount `json:"hosts"`
}

// ErrAuthRequired is returned by ensureAuth when the operator is not logged
// in to gh and --non-interactive forbids starting the device flow. The
// caller maps this to exit code 2.
var ErrAuthRequired = errors.New("not logged in to github.com; run: gh auth login")

// ghAuthStatus runs `gh auth status --json hosts` and returns every
// account of every host, hosts in name order. A non-zero exit or malformed
// JSON is a hard failure; no accounts is a normal "logged out" result at
// exit 0 (gh auth status can exit 0 while still describing an error state,
// so codvps checks the payload rather than trusting the exit code alone).
func ghAuthStatus(ctx *Context) ([]ghAccount, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("gh", "auth", "status", "--json", "hosts")
	if exitCode != 0 || err != nil {
		// Older gh (Ubuntu 24.04 ships 2.45) has no --json on auth status;
		// the guided flow upgrades it before this point (ensureGH).
		if ghcli.Classify(stderr, exitCode, err) == ghcli.TooOld {
			return nil, fmt.Errorf("this gh is too old for codvps: `gh auth status --json` is unsupported; run sudo codvps install, or install the current gh from %s", ghcli.PackagesURL)
		}
		return nil, fmt.Errorf("gh auth status failed: exit %d: %s", exitCode, strings.TrimSpace(stderr))
	}
	var out ghAuthStatusOutput
	if strings.TrimSpace(stdout) == "" {
		return nil, nil
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return nil, fmt.Errorf("gh auth status returned malformed JSON: %w", err)
	}
	hosts := make([]string, 0, len(out.Hosts))
	for host := range out.Hosts {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	var accounts []ghAccount
	for _, host := range hosts {
		for _, a := range out.Hosts[host] {
			if a.Host == "" {
				a.Host = host
			}
			accounts = append(accounts, a)
		}
	}
	return accounts, nil
}

// activeGitHubAccount returns the active github.com account, if any.
func activeGitHubAccount(accounts []ghAccount) (ghAccount, bool) {
	for _, a := range accounts {
		if a.Active && (a.Host == "" || a.Host == "github.com") {
			return a, true
		}
	}
	return ghAccount{}, false
}

// ensureAuth is step (a): confirm the operator is logged in to gh.
// nonInteractive is only the explicit --non-interactive request: it makes
// a logged-out run stop with ErrAuthRequired. Otherwise the device flow
// starts, with or without a terminal, because it needs none: gh prints the
// one-time code and URL for any device's browser.
func ensureAuth(ctx *Context, nonInteractive bool) (Status, string, error) {
	accounts, err := ghAuthStatus(ctx)
	if err != nil {
		return StatusFailed, "", err
	}
	if account, ok := activeGitHubAccount(accounts); ok {
		return StatusOK, fmt.Sprintf("logged in as %s (%s)", account.Login, credentialStorageLabel(account)), nil
	}

	if nonInteractive {
		return StatusPending, "not logged in", ErrAuthRequired
	}

	// Log in now with GitHub's device code flow, since a headless host has
	// no browser. --web picks the browser/device flow; gh gets an empty,
	// non-terminal stdin, so it neither prompts nor waits for Enter (and
	// never touches the clipboard: --clipboard is not passed): it prints
	// the one-time code and the github.com/login/device URL and waits until
	// the code is entered on any device. Its output streams live to
	// stdout/stderr, so a caller without a terminal (docker exec, ssh
	// without -t, a script) sees the code while gh waits. --skip-ssh-key keeps gh from generating or uploading a key of
	// its own (codvps owns key selection in the steps that follow), so the
	// key scope codvps needs to register one is requested explicitly.
	ctx.printf("GitHub authentication required. Open https://github.com/login/device on any device and enter the one-time code gh prints below.\n")
	exitCode, runErr := ctx.Runner.RunWithIO("gh", ghDeviceLoginArgs, strings.NewReader(""), ctx.out(), ctx.errOut())
	if exitCode != 0 || runErr != nil {
		return StatusFailed, "", runFailure("gh auth login", exitCode, runErr)
	}

	accounts, err = ghAuthStatus(ctx)
	if err != nil {
		return StatusFailed, "", err
	}
	account, ok := activeGitHubAccount(accounts)
	if !ok {
		return StatusFailed, "", errors.New("gh auth login reported success but no active github.com account was found")
	}
	return StatusOK, fmt.Sprintf("logged in as %s (%s)", account.Login, credentialStorageLabel(account)), nil
}

// ghDeviceLoginArgs is `gh auth login` for a headless host: the device
// code flow for github.com with SSH as the git protocol, no gh-managed key,
// and the scope codvps needs to register its own key.
var ghDeviceLoginArgs = []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "ssh", "--skip-ssh-key", "--web", "--scopes", "admin:public_key"}

// credentialStorageLabel reports how the account's credential is stored
// without ever reading the token itself: "env" when GH_TOKEN/GITHUB_TOKEN
// is set (gh always prefers an env override), the tokenSource gh itself
// reported otherwise, or "unknown".
func credentialStorageLabel(account ghAccount) string {
	if os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" {
		return "env"
	}
	// gh names the source: "keyring", the variable a token came from, or
	// the path of the hosts file holding it.
	switch src := account.TokenSource; {
	case src == "keyring":
		return "keyring"
	case src == "GH_TOKEN" || src == "GITHUB_TOKEN" || src == "GH_ENTERPRISE_TOKEN" || src == "GITHUB_ENTERPRISE_TOKEN":
		return "env"
	case strings.HasPrefix(src, "/"):
		return "file"
	default:
		return "unknown"
	}
}

// hasRequiredKeyScope reports whether the account's token carries a scope
// that allows registering an SSH key.
func hasRequiredKeyScope(account ghAccount) bool {
	for _, s := range account.Scopes {
		if s == "admin:public_key" || s == "write:public_key" {
			return true
		}
	}
	return false
}

// ghAPIUserField runs `gh api user --jq <expr>` and returns the trimmed
// result. Used to look up the fields needed to derive a noreply email or
// confirm the SSH login matches the gh account, without ever touching a
// credential file.
func ghAPIUserField(ctx *Context, jqExpr string) (string, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("gh", "api", "user", "--jq", jqExpr)
	if exitCode != 0 || err != nil {
		return "", fmt.Errorf("gh api user --jq %q failed: exit %d: %s", jqExpr, exitCode, strings.TrimSpace(stderr))
	}
	return strings.TrimSpace(stdout), nil
}
