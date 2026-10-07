package login

import (
	"fmt"
	"strings"
)

// ensureGitIdentity is step (f): set git's global user.name/user.email if
// either is unset, from the explicit --identity-name/--identity-email
// flags. An already-configured field is never overwritten, on any run,
// with any flags. When only one of the two is given and the other is
// unset, the missing half is derived from the gh account (a noreply email
// from login+id, or the account's display name). With neither an existing
// identity nor any --identity-* flag, both fields are left completely
// untouched and the step is reported pending.
func ensureGitIdentity(ctx *Context, identityName, identityEmail string) (Status, string, error) {
	existingName, nameSet, err := gitConfigGet(ctx, "user.name")
	if err != nil {
		return StatusFailed, "", err
	}
	existingEmail, emailSet, err := gitConfigGet(ctx, "user.email")
	if err != nil {
		return StatusFailed, "", err
	}

	if nameSet && emailSet {
		return StatusOK, fmt.Sprintf("already configured (%s <%s>)", existingName, existingEmail), nil
	}

	newName := identityName
	newEmail := identityEmail

	if !nameSet && newName == "" && newEmail != "" {
		if derived, derr := deriveGitName(ctx); derr == nil && derived != "" {
			newName = derived
		}
	}
	if !emailSet && newEmail == "" && newName != "" {
		if derived, derr := deriveNoreplyEmail(ctx); derr == nil && derived != "" {
			newEmail = derived
		}
	}

	if !nameSet && newName != "" {
		if err := gitConfigSet(ctx, "user.name", newName); err != nil {
			return StatusFailed, "", err
		}
		existingName = newName
		nameSet = true
	}
	if !emailSet && newEmail != "" {
		if err := gitConfigSet(ctx, "user.email", newEmail); err != nil {
			return StatusFailed, "", err
		}
		existingEmail = newEmail
		emailSet = true
	}

	if nameSet && emailSet {
		return StatusOK, fmt.Sprintf("configured (%s <%s>)", existingName, existingEmail), nil
	}

	var missing []string
	if !nameSet {
		missing = append(missing, "user.name (--identity-name)")
	}
	if !emailSet {
		missing = append(missing, "user.email (--identity-email)")
	}
	return StatusPending, "missing " + strings.Join(missing, ", "), nil
}

// gitConfigGet reads a global git config key. A key that is simply unset
// (git config's exit 1) is not an error; any other failure is.
func gitConfigGet(ctx *Context, key string) (value string, isSet bool, err error) {
	stdout, stderr, exitCode, runErr := ctx.Runner.Run("git", "config", "--global", "--get", key)
	switch {
	case exitCode == 0 && runErr == nil:
		return strings.TrimSpace(stdout), true, nil
	case exitCode == 1 && runErr != nil:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("git config --get %s failed: exit %d: %s", key, exitCode, strings.TrimSpace(stderr))
	}
}

func gitConfigSet(ctx *Context, key, value string) error {
	_, stderr, exitCode, err := ctx.Runner.Run("git", "config", "--global", key, value)
	if exitCode != 0 || err != nil {
		return fmt.Errorf("git config %s failed: exit %d: %s", key, exitCode, strings.TrimSpace(stderr))
	}
	return nil
}

// deriveNoreplyEmail builds GitHub's noreply address from the account's
// numeric id and login: "<id>+<login>@users.noreply.github.com".
func deriveNoreplyEmail(ctx *Context) (string, error) {
	id, err := ghAPIUserField(ctx, ".id")
	if err != nil {
		return "", err
	}
	login, err := ghAPIUserField(ctx, ".login")
	if err != nil {
		return "", err
	}
	if id == "" || login == "" {
		return "", fmt.Errorf("gh api user returned an empty id or login")
	}
	return fmt.Sprintf("%s+%s@users.noreply.github.com", id, login), nil
}

// deriveGitName uses the gh account's display name, falling back to its
// login when no display name is set.
func deriveGitName(ctx *Context) (string, error) {
	name, err := ghAPIUserField(ctx, ".name // .login")
	if err != nil {
		return "", err
	}
	return name, nil
}
