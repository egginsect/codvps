package login

import (
	"fmt"
	"os"
	"strings"
)

// ensureSSHKeyRegistered is step (c): register the selected key with
// GitHub if it is not already registered. selectedKey is the private key
// path; the public key at selectedKey+".pub" is what gets registered.
func ensureSSHKeyRegistered(ctx *Context, nonInteractive bool, selectedKey string, yes bool) (Status, string, error) {
	pubKeyPath := selectedKey + ".pub"
	pubKey, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return StatusFailed, "", fmt.Errorf("failed to read %s: %w", pubKeyPath, err)
	}

	registered, err := sshKeyRegisteredContent(ctx, string(pubKey))
	if err != nil {
		// "Registration stops if listing keys fails": a hard failure, not a
		// silent "treat as unregistered".
		return StatusFailed, "", fmt.Errorf("could not check existing registration: %w", err)
	}
	if registered {
		return StatusOK, "already registered", nil
	}

	accounts, err := ghAuthStatus(ctx)
	if err != nil {
		return StatusFailed, "", err
	}
	account, ok := activeGitHubAccount(accounts)
	if !ok {
		// Never enroll a key for an unknown account.
		return StatusFailed, "", fmt.Errorf("no active github.com account to register the key against")
	}
	if !hasRequiredKeyScope(account) {
		return StatusPending, fmt.Sprintf("token for %s lacks admin:public_key/write:public_key; run: gh auth refresh --scopes admin:public_key", account.Login), nil
	}

	if nonInteractive && !yes {
		return StatusPending, "registration requires --yes (non-interactive)", nil
	}

	ctx.printf("Registering SSH key with GitHub for %s:\n  %s", account.Login, string(pubKey))

	title := "codvps-" + account.Login
	stdout, stderr, exitCode, err := ctx.Runner.Run("gh", "ssh-key", "add", pubKeyPath, "--title", title)
	if stdout != "" {
		ctx.printf("%s", stdout)
	}
	if exitCode != 0 || err != nil {
		if stderr != "" {
			ctx.errPrintf("%s", stderr)
		}
		return StatusFailed, "", runFailure("gh ssh-key add", exitCode, err)
	}
	return StatusOK, "registered as " + title, nil
}

// sshKeyRegistered reports whether the key at pubKeyPath is already
// registered with GitHub.
func sshKeyRegistered(ctx *Context, pubKeyPath string) (bool, error) {
	pubKey, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return false, fmt.Errorf("failed to read %s: %w", pubKeyPath, err)
	}
	return sshKeyRegisteredContent(ctx, string(pubKey))
}

// sshKeyRegisteredContent lists the operator's registered SSH keys via
// `gh ssh-key list` and checks whether one of them matches pubKeyContent
// (the key-material field, not the whole TSV row, so a differing title
// never causes a false negative).
func sshKeyRegisteredContent(ctx *Context, pubKeyContent string) (bool, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("gh", "ssh-key", "list")
	if exitCode != 0 || err != nil {
		return false, fmt.Errorf("gh ssh-key list failed: exit %d: %s", exitCode, strings.TrimSpace(stderr))
	}
	wantMaterial := keyMaterial(pubKeyContent)
	if wantMaterial == "" {
		return false, nil
	}
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		if keyMaterial(fields[1]) == wantMaterial {
			return true, nil
		}
	}
	return false, nil
}

// keyMaterial extracts the base64 key-data field of an "ssh-<type> <data>
// [comment]" public key line, ignoring any trailing comment so a key
// registered under a different comment/title still matches.
func keyMaterial(pubKey string) string {
	fields := strings.Fields(strings.TrimSpace(pubKey))
	if len(fields) < 2 {
		return ""
	}
	return fields[0] + " " + fields[1]
}
