package login

import (
	"fmt"
	"regexp"
	"strings"
)

var hiPattern = regexp.MustCompile(`Hi ([^!]+)!`)

// verify is step (g): confirm SSH access to GitHub actually works. It
// always attempts `ssh -T git@github.com`; the orchestrator (GitHub), not
// this function, is what skips the whole step when the key step ended
// pending or skipped, so a missing key choice never turns into a failing
// SSH check or a false "Ready".
//
// GitHub's SSH endpoint always exits 1 on success ("Hi <login>! ..." is not
// an error, it is the whole point of `ssh -T git@github.com`), so success
// is judged by that message and by the login inside it matching the gh
// account, never by the exit code alone.
//
// When a specific key was selected and the effective ssh config (per
// `ssh -G github.com`) does not already route to it, the connection is
// pinned to that key with -i/-o IdentitiesOnly=yes.
func verify(ctx *Context, selectedKey string) (Status, string, error) {
	expectedLogin, err := ghAPIUserField(ctx, ".login")
	if err != nil {
		return StatusFailed, "", err
	}

	var args []string
	if selectedKey != "" {
		identityFile, ideErr := effectiveIdentityFile(ctx)
		if ideErr != nil || identityFile != selectedKey {
			args = append(args, "-i", selectedKey, "-o", "IdentitiesOnly=yes")
		}
	}
	args = append(args, "-T", "git@"+knownHost)

	// GitHub's own SSH endpoint always exits 1 on the success banner, so the
	// exit code itself is not the success signal; it is still inspected
	// below so a genuine Runner-level failure (ssh missing, etc.) is
	// reported with that detail rather than swallowed. Real OpenSSH prints
	// the "Hi <login>! ..." banner to stderr, not stdout (this is what a
	// plain `ssh -T git@github.com` on a real terminal shows), so both
	// streams are searched together, matching the reference's `2>&1`.
	stdout, stderr, exitCode, runErr := ctx.Runner.Run("ssh", args...)
	combined := stdout + stderr
	m := hiPattern.FindStringSubmatch(combined)
	if m == nil {
		if runErr != nil && strings.TrimSpace(combined) == "" {
			return StatusFailed, "", fmt.Errorf("ssh verification failed: exit %d: %w", exitCode, runErr)
		}
		return StatusFailed, "", fmt.Errorf("ssh verification failed: %s", strings.TrimSpace(combined))
	}
	if m[1] != expectedLogin {
		return StatusFailed, "", fmt.Errorf("ssh verification connected as %q, expected the gh account %q", m[1], expectedLogin)
	}
	return StatusOK, "authenticated as " + expectedLogin, nil
}
