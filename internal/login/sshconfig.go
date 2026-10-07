package login

import (
	"fmt"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
)

// ensureSSHConfig is step (e): add a scoped `Host github.com` block that
// points at the selected key, but only when one was selected and only when
// no such block exists yet. It never adds an insteadOf rewrite and never
// touches ~/.ssh/config when no dedicated key was chosen.
func ensureSSHConfig(ctx *Context, selectedKey string) (Status, string, error) {
	if selectedKey == "" {
		return StatusSkipped, "no dedicated key selected", nil
	}

	configPath := ctx.sshDir() + "/config"
	content, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return StatusFailed, "", fmt.Errorf("failed to read %s: %w", configPath, err)
	}
	if hasGitHubHostBlock(string(content)) {
		return StatusOK, "Host github.com block already present", nil
	}

	if err := os.MkdirAll(ctx.sshDir(), 0o700); err != nil {
		return StatusFailed, "", fmt.Errorf("failed to create %s: %w", ctx.sshDir(), err)
	}
	block := fmt.Sprintf("\nHost github.com\n  HostName github.com\n  User git\n  IdentityFile %s\n  IdentitiesOnly yes\n", selectedKey)
	newContent := string(content) + block
	if err := fsutil.AtomicWrite(configPath, []byte(newContent), 0o600); err != nil {
		return StatusFailed, "", fmt.Errorf("failed to write %s: %w", configPath, err)
	}
	return StatusOK, "added Host github.com with IdentityFile " + selectedKey, nil
}

// hasGitHubHostBlock reports whether ssh config content already has a
// `Host github.com` block (case-insensitive, as ssh_config directives are).
func hasGitHubHostBlock(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "Host") {
			for _, pattern := range fields[1:] {
				if strings.EqualFold(pattern, knownHost) {
					return true
				}
			}
		}
	}
	return false
}

// effectiveIdentityFile runs `ssh -G github.com` and returns the
// IdentityFile ssh would actually use, so codvps can confirm the config it
// wrote (or found) is the config that is actually in effect, rather than
// merely that some `Host github.com` block exists somewhere.
func effectiveIdentityFile(ctx *Context) (string, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("ssh", "-G", knownHost)
	if exitCode != 0 || err != nil {
		return "", fmt.Errorf("ssh -G %s failed: exit %d: %s", knownHost, exitCode, strings.TrimSpace(stderr))
	}
	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.EqualFold(fields[0], "identityfile") {
			return fields[1], nil
		}
	}
	return "", nil
}
