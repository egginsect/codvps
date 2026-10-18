package login

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const githubAuthOnlyHint = "run: codvps login github --auth-only"

// GitHubAuthOnly authenticates HTTPS Git access without provisioning the
// host, touching SSH, or changing Git identity. Explicit read-only modes
// never start login or write gh/Git configuration. gh 2.46 is supported:
// status is checked by its exit code, not the newer --json interface.
func GitHubAuthOnly(ctx *Context, nonInteractive, deferOnly bool) (int, error) {
	login, authenticated, err := githubHTTPSAccount(ctx)
	if deferOnly {
		switch {
		case err != nil:
			ctx.printf("GitHub HTTPS preflight: %s\n", err)
		case authenticated:
			ctx.printf("GitHub HTTPS preflight: authenticated as %s.\n", login)
		default:
			ctx.printf("GitHub HTTPS preflight: authentication required; %s.\n", githubAuthOnlyHint)
		}
		ctx.printf("Read-only preflight; no login or configuration changes.\n")
		return 0, nil
	}
	if err != nil {
		return 1, err
	}
	if !authenticated {
		if nonInteractive {
			return 2, fmt.Errorf("GitHub authentication is missing or could not be verified; %s", githubAuthOnlyHint)
		}
		ctx.printf("GitHub HTTPS authentication required. Open https://github.com/login/device and enter the one-time code gh prints below.\n")
		code, err := ctx.Runner.RunWithIO("gh", []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--skip-ssh-key", "--web"}, strings.NewReader(""), ctx.out(), ctx.errOut())
		if code != 0 || err != nil {
			return 1, fmt.Errorf("GitHub HTTPS login failed; %s", githubAuthOnlyHint)
		}
		login, authenticated, err = githubHTTPSAccount(ctx)
		if err != nil {
			return 1, err
		}
		if !authenticated {
			return 1, fmt.Errorf("GitHub HTTPS login finished but authentication could not be verified; %s", githubAuthOnlyHint)
		}
	}
	if nonInteractive {
		ctx.printf("GitHub authentication verified as %s. Read-only check; credential configuration unchanged.\n", login)
		return 0, nil
	}

	// An already-authenticated account can still prefer SSH. Configure only
	// this host's protocol, then let gh install its HTTPS credential helper.
	// Capture and discard subprocess diagnostics: none should expose tokens.
	_, _, code, err := ctx.Runner.Run("gh", "config", "set", "git_protocol", "https", "--host", "github.com")
	if code != 0 || err != nil {
		return 1, fmt.Errorf("could not configure GitHub HTTPS protocol; check the gh configuration directory is writable, then %s", githubAuthOnlyHint)
	}
	_, _, code, err = ctx.Runner.Run("gh", "auth", "setup-git", "--hostname", "github.com")
	if code != 0 || err != nil {
		return 1, fmt.Errorf("could not configure the GitHub HTTPS credential helper; check the Git configuration path is writable, then %s", githubAuthOnlyHint)
	}
	ctx.printf("GitHub HTTPS authentication ready as %s; gh Git credential helper configured.\n", login)
	return 0, nil
}

// githubHTTPSAccount never reads credentials or relays auth-status output.
// The API's explicitly selected public username is the only output used.
func githubHTTPSAccount(ctx *Context) (login string, authenticated bool, err error) {
	_, _, code, runErr := ctx.Runner.Run("gh", "auth", "status", "--hostname", "github.com")
	if errors.Is(runErr, exec.ErrNotFound) {
		return "", false, errors.New("gh is not installed or not on PATH; install GitHub CLI, then " + githubAuthOnlyHint)
	}
	if code != 0 || runErr != nil {
		return "", false, nil
	}
	stdout, _, code, runErr := ctx.Runner.Run("gh", "api", "--hostname", "github.com", "user", "--jq", ".login")
	if code != 0 || runErr != nil {
		return "", false, errors.New("could not verify the GitHub account; check network access and gh authentication, then " + githubAuthOnlyHint)
	}
	login = strings.TrimSpace(stdout)
	if !validGitHubLogin(login) {
		return "", false, errors.New("GitHub returned an invalid account name; " + githubAuthOnlyHint)
	}
	return login, true, nil
}

// Reject unexpected API output rather than rendering arbitrary content from
// a subprocess. GitHub personal usernames contain up to 39 ASCII letters,
// digits and hyphens, with no leading or trailing hyphen.
func validGitHubLogin(login string) bool {
	if len(login) == 0 || len(login) > 39 || login[0] == '-' || login[len(login)-1] == '-' {
		return false
	}
	for _, c := range login {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
