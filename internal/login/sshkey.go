package login

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// ensureSSHKey is step (b): choose or generate the SSH key that will
// represent this host to GitHub. keyChoice is "", "skip", "generate" or
// "existing:<path>". It returns the selected key's private-key path (empty
// when skipped or left pending).
func ensureSSHKey(ctx *Context, nonInteractive bool, keyChoice string, passphraseEmpty bool) (string, Status, string, error) {
	switch {
	case keyChoice == "skip":
		return "", StatusSkipped, "skipped by --key skip", nil

	case keyChoice == "generate":
		return ensureSSHKeyGenerate(ctx, nonInteractive || !ctx.IsTTY(), passphraseEmpty)

	case strings.HasPrefix(keyChoice, "existing:"):
		path := strings.TrimPrefix(keyChoice, "existing:")
		return ensureSSHKeyExisting(ctx, path)

	case keyChoice != "":
		return "", StatusFailed, "", fmt.Errorf("unrecognized --key choice %q", keyChoice)
	}

	// No explicit choice. nonInteractive is the explicit --non-interactive
	// request. Without a terminal, or with --yes, the prompts below take
	// their default answers instead of being asked.
	if nonInteractive {
		return "", StatusPending, "no --key choice given; pass --key skip|generate|existing:<path>", nil
	}
	if ctx.AssumeYes || !ctx.IsTTY() {
		return ensureSSHKeyDefaults(ctx, passphraseEmpty)
	}
	return ensureSSHKeyInteractive(ctx, passphraseEmpty)
}

// ensureSSHKeyGenerate creates the codvps-managed key pair if it does not
// already exist. A second, idempotent call never regenerates it.
func ensureSSHKeyGenerate(ctx *Context, nonInteractive bool, passphraseEmpty bool) (string, Status, string, error) {
	keyPath := ctx.keyPath()

	if fileExists(keyPath) {
		return keyPath, StatusOK, "already exists at " + keyPath, nil
	}

	if nonInteractive && !passphraseEmpty {
		return "", StatusFailed, "", errors.New("generating a key non-interactively requires --passphrase-empty")
	}

	if err := generateKeyPair(ctx, keyPath, true); err != nil {
		return "", StatusFailed, "", err
	}
	return keyPath, StatusOK, "generated at " + keyPath, nil
}

// ensureSSHKeyExisting validates and selects an operator-provided key path.
// Neither the private nor the public key file may be a symlink: codvps
// registers exactly the bytes it can see, not whatever a symlink might be
// swapped to point at later.
func ensureSSHKeyExisting(ctx *Context, path string) (string, Status, string, error) {
	if path == "" {
		return "", StatusFailed, "", errors.New("--key existing: requires a path")
	}
	if err := requireRegularFile(path); err != nil {
		return "", StatusFailed, "", fmt.Errorf("existing key %q: %w", path, err)
	}
	pubPath := path + ".pub"
	if err := requireRegularFile(pubPath); err != nil {
		return "", StatusFailed, "", fmt.Errorf("existing key %q: missing or invalid public key: %w", path, err)
	}
	return path, StatusOK, "using existing key at " + path, nil
}

// ensureSSHKeyInteractive prompts the operator when attached to a real
// terminal: reuse the codvps-managed key if it exists, otherwise offer to
// generate one, honoring a decline (skip) and a passphrase choice.
//
// codvps never asks the operator to type their passphrase to it, and never
// reads one: the second question here is only the yes/no choice of whether
// the key gets an empty passphrase. Answering no hands the terminal
// straight to ssh-keygen itself (see generateKeyPair), which prompts for
// and confirms the real passphrase without codvps ever seeing it.
func ensureSSHKeyInteractive(ctx *Context, passphraseEmpty bool) (string, Status, string, error) {
	keyPath := ctx.keyPath()
	if fileExists(keyPath) {
		return keyPath, StatusOK, "already exists at " + keyPath, nil
	}

	br := bufio.NewReader(ctx.Stdin)

	ctx.printf("No GitHub SSH key found. Generate one at %s? [Y/n] ", keyPath)
	answer := readLine(br)
	if isNo(answer) {
		return "", StatusSkipped, "operator declined key generation", nil
	}

	if !passphraseEmpty {
		ctx.printf("Use an empty passphrase? Answering no lets ssh-keygen prompt you on this terminal directly; codvps itself never reads a passphrase. [y/N] ")
		passphraseEmpty = isYes(readLine(br))
	}

	if err := generateKeyPair(ctx, keyPath, passphraseEmpty); err != nil {
		return "", StatusFailed, "", err
	}
	return keyPath, StatusOK, "generated at " + keyPath, nil
}

// assumeReason says why a prompt is answered without asking.
func (ctx *Context) assumeReason() string {
	if ctx.AssumeYes {
		return "--yes"
	}
	return "no terminal"
}

// ensureSSHKeyDefaults is ensureSSHKeyInteractive without asking: each
// prompt takes its default answer and says so. The default of the first
// ([Y/n]) is to generate the key. The second ([y/N]) defaults to a real
// passphrase, which ssh-keygen can only ask for on a terminal; an empty
// passphrase is the non-default, so it is never assumed and needs
// --passphrase-empty.
func ensureSSHKeyDefaults(ctx *Context, passphraseEmpty bool) (string, Status, string, error) {
	keyPath := ctx.keyPath()
	if fileExists(keyPath) {
		return keyPath, StatusOK, "already exists at " + keyPath, nil
	}
	ctx.printf("assuming yes: generate SSH key at %s (%s)\n", keyPath, ctx.assumeReason())
	if !passphraseEmpty {
		if !ctx.IsTTY() {
			return "", StatusPending, "the key passphrase can only be entered on a terminal; pass --passphrase-empty to generate it without one, or --key skip", nil
		}
		ctx.printf("assuming no: empty passphrase (%s); ssh-keygen will ask for a passphrase\n", ctx.assumeReason())
	}
	if err := generateKeyPair(ctx, keyPath, passphraseEmpty); err != nil {
		return "", StatusFailed, "", err
	}
	return keyPath, StatusOK, "generated at " + keyPath, nil
}

// generateKeyPair runs ssh-keygen to create an ed25519 key pair at keyPath.
//
// A non-empty passphrase is never read or held by codvps: -N is passed only
// for the explicit empty choice ("-N \"\""); otherwise -N is omitted
// entirely and ssh-keygen is run attached to ctx's own stdin/stdout/stderr
// (RunWithIO), so ssh-keygen prompts for and confirms the passphrase on the
// real terminal itself. A passphrase typed there flows straight through the
// OS pipe between the terminal and the ssh-keygen child process; it is
// never captured into a Go value, logged, or placed on the command line
// (which would otherwise leak through /proc/<pid>/cmdline).
//
// ssh-keygen's own progress/fingerprint/randomart output goes straight to
// ctx.Stdout; the caller learns the selected key only from keyPath, never
// by parsing that output.
func generateKeyPair(ctx *Context, keyPath string, emptyPassphrase bool) error {
	if err := os.MkdirAll(ctx.sshDir(), 0o700); err != nil {
		return fmt.Errorf("failed to create %s: %w", ctx.sshDir(), err)
	}

	args := []string{"-t", "ed25519", "-f", keyPath, "-C", "codvps-github"}

	var exitCode int
	var err error
	if emptyPassphrase {
		args = append(args, "-N", "")
		var stdout, stderr string
		stdout, stderr, exitCode, err = ctx.Runner.Run("ssh-keygen", args...)
		if stdout != "" {
			ctx.printf("%s", stdout)
		}
		if (exitCode != 0 || err != nil) && stderr != "" {
			ctx.errPrintf("%s", stderr)
		}
	} else {
		exitCode, err = ctx.Runner.RunWithIO("ssh-keygen", args, ctx.Stdin, ctx.out(), ctx.errOut())
	}
	if exitCode != 0 || err != nil {
		_ = os.Remove(keyPath)
		_ = os.Remove(keyPath + ".pub")
		return runFailure("ssh-keygen", exitCode, err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		return fmt.Errorf("failed to set mode on %s: %w", keyPath, err)
	}
	return nil
}

// keyFingerprint runs `ssh-keygen -l -f <path>` and returns the SHA256:...
// fingerprint field, used only for the read-only preflight report.
func keyFingerprint(ctx *Context, pubKeyPath string) (string, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("ssh-keygen", "-l", "-f", pubKeyPath)
	if exitCode != 0 || err != nil {
		return "", fmt.Errorf("ssh-keygen -l -f %s failed: exit %d: %s", pubKeyPath, exitCode, strings.TrimSpace(stderr))
	}
	fields := strings.Fields(stdout)
	for _, f := range fields {
		if strings.HasPrefix(f, "SHA256:") {
			return f, nil
		}
	}
	return strings.TrimSpace(stdout), nil
}

func fileExists(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// requireRegularFile fails closed on a missing file, a directory, or a
// symlink (of any depth: os.Lstat, not os.Stat, so a symlink is rejected
// even if it resolves to a regular file).
func requireRegularFile(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("not found: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("must not be a symlink")
	}
	if !fi.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	return nil
}

func readLine(br *bufio.Reader) string {
	line, _ := br.ReadString('\n')
	return strings.TrimRight(line, "\r\n")
}

func isNo(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "n" || a == "no"
}

func isYes(answer string) bool {
	a := strings.ToLower(strings.TrimSpace(answer))
	return a == "y" || a == "yes"
}

// candidateKeys lists the private key files under ~/.ssh that have a
// matching .pub file, for the read-only preflight report. It never reads
// key contents beyond computing a fingerprint.
func candidateKeys(ctx *Context) []string {
	entries, err := os.ReadDir(ctx.sshDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		full := ctx.sshDir() + "/" + e.Name()
		if fileExists(full) && fileExists(full+".pub") {
			out = append(out, full)
		}
	}
	return out
}
