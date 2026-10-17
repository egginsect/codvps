package cli

import (
	"regexp"
	"strings"

	"github.com/egginsect/codvps/internal/runner"
)

// Match only conventional GitHub SSH remotes with literal owner/repository
// paths. In particular, do not reinterpret SSH aliases, users, ports, URL
// credentials, escapes, query strings, fragments or local filesystem paths.
var githubSSHRemote = regexp.MustCompile(`^(?:git@(?i:github\.com):|ssh://git@(?i:github\.com)/)([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?/[A-Za-z0-9._-]+)(/?)$`)

// repoCloneURL honors gh's HTTPS preference for conventional GitHub SSH
// remotes, so an existing HTTPS login can also clone a pasted SSH URL. The
// runner must use the same configured child environment as Git. This probe
// reads only a nonsecret setting; it never logs in or changes configuration.
// Missing gh, failed probes and other protocol values retain the input.
func repoCloneURL(raw string, r runner.Runner) string {
	match := githubSSHRemote.FindStringSubmatch(raw)
	if match == nil {
		return raw
	}
	_, name, _ := strings.Cut(match[1], "/")
	switch strings.TrimSuffix(name, ".git") {
	case "", ".", "..":
		return raw
	}
	protocol, _, code, err := r.Run("gh", "config", "get", "git_protocol", "--host", "github.com")
	if err != nil || code != 0 || strings.TrimSpace(protocol) != "https" {
		return raw
	}
	return "https://github.com/" + match[1] + match[2]
}
