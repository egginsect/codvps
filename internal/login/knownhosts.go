package login

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // known_hosts hashing is HMAC-SHA1 by the OpenSSH format itself, not a security choice made here.
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
)

const knownHost = "github.com"

// ensureKnownHosts is step (d): make sure known_hosts has an entry for
// every key GitHub currently publishes at `gh api meta`. There is
// deliberately no trust-on-first-use fallback that probes the host
// directly for its keys anywhere in this package. Existing entries — plain
// or hashed — are never rewritten; only genuinely missing published keys
// are appended. An existing github.com entry whose key GitHub does not
// publish is refused and the file is left byte-for-byte unchanged.
func ensureKnownHosts(ctx *Context) (Status, string, error) {
	published, err := fetchGitHubHostKeys(ctx)
	if err != nil {
		return StatusFailed, "", fmt.Errorf("could not fetch GitHub's published host keys: %w", err)
	}
	if len(published) == 0 {
		return StatusFailed, "", errors.New("gh api meta returned no ssh_keys")
	}

	knownHostsPath := ctx.sshDir() + "/known_hosts"
	content, err := os.ReadFile(knownHostsPath)
	if err != nil && !os.IsNotExist(err) {
		return StatusFailed, "", fmt.Errorf("failed to read %s: %w", knownHostsPath, err)
	}

	lines := splitLines(string(content))
	present := map[string]bool{} // key material already covered by an existing entry
	for _, line := range lines {
		hostsField, keyMaterial, ok := parseKnownHostsLine(line)
		if !ok {
			continue
		}
		if !hostsFieldMatches(hostsField, knownHost) {
			continue
		}
		if !isPublishedMaterial(published, keyMaterial) {
			return StatusFailed, "", fmt.Errorf("known_hosts has a github.com key GitHub does not publish; refusing to touch the file")
		}
		present[keyMaterial] = true
	}

	var toAppend []string
	for _, pk := range published {
		if !present[keyMaterial(pk)] {
			toAppend = append(toAppend, knownHost+" "+pk)
		}
	}
	if len(toAppend) == 0 {
		return StatusOK, "already up to date", nil
	}

	if err := os.MkdirAll(ctx.sshDir(), 0o700); err != nil {
		return StatusFailed, "", fmt.Errorf("failed to create %s: %w", ctx.sshDir(), err)
	}
	newContent := string(content)
	if newContent != "" && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}
	newContent += strings.Join(toAppend, "\n") + "\n"
	if err := fsutil.AtomicWrite(knownHostsPath, []byte(newContent), 0o600); err != nil {
		return StatusFailed, "", fmt.Errorf("failed to write %s: %w", knownHostsPath, err)
	}
	return StatusOK, fmt.Sprintf("added %d entr(y/ies)", len(toAppend)), nil
}

// fetchGitHubHostKeys runs `gh api meta --jq '.ssh_keys[]'` and returns one
// "<keytype> <base64-data>" string per published key.
func fetchGitHubHostKeys(ctx *Context) ([]string, error) {
	stdout, stderr, exitCode, err := ctx.Runner.Run("gh", "api", "meta", "--jq", ".ssh_keys[]")
	if exitCode != 0 || err != nil {
		return nil, fmt.Errorf("gh api meta failed: exit %d: %s", exitCode, strings.TrimSpace(stderr))
	}
	var out []string
	for _, line := range splitLines(stdout) {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// parseKnownHostsLine splits a known_hosts line into its hosts field and
// "<keytype> <data>" key material, skipping blank lines and comments.
func parseKnownHostsLine(line string) (hostsField, material string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", "", false
	}
	return fields[0], fields[1] + " " + fields[2], true
}

// hostsFieldMatches reports whether a known_hosts hosts field (plain,
// comma-separated, or "|1|salt|hash" hashed) covers host.
func hostsFieldMatches(hostsField, host string) bool {
	if strings.HasPrefix(hostsField, "|1|") {
		return hashedHostMatches(hostsField, host)
	}
	for _, h := range strings.Split(hostsField, ",") {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// hashedHostMatches implements OpenSSH's HashKnownHosts algorithm:
// "|1|<base64 salt>|<base64 HMAC-SHA1(salt, host)>". This lets codvps
// recognize an already-hashed entry without ssh-keygen -F and without ever
// un-hashing the file.
func hashedHostMatches(hostsField, host string) bool {
	parts := strings.Split(hostsField, "|")
	if len(parts) != 4 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	mac := hmac.New(sha1.New, salt)
	mac.Write([]byte(host))
	return hmac.Equal(mac.Sum(nil), want)
}

func isPublishedMaterial(published []string, material string) bool {
	for _, pk := range published {
		if keyMaterial(pk) == material {
			return true
		}
	}
	return false
}
