package repo

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Only conventional GitHub HTTPS/SSH origins are accepted. Credentials,
// arbitrary remote helpers, aliases, explicit ports and URL suffixes are not.
var localGitHubOrigin = regexp.MustCompile(`^(?:https://(?i:github\.com)/|git@(?i:github\.com):|ssh://git@(?i:github\.com)/)([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)/([A-Za-z0-9._-]+)/?$`)

// AddLocal registers an existing primary checkout without network operations
// or provider reconciliation. Its origin is a local assertion, not proof of
// remote existence, account access or ownership. No missing path is cloned.
func (rm *RepoManager) AddLocal(dest string) error {
	if err := rm.validateLocalCheckout(dest); err != nil {
		return err
	}
	out, _, code, err := rm.git.Run("git", "-C", dest, "remote", "get-url", "origin")
	if err != nil || code != 0 {
		return fmt.Errorf("local checkout must have an origin remote")
	}
	origin := strings.TrimSpace(out)
	match := localGitHubOrigin.FindStringSubmatch(origin)
	if match == nil {
		return fmt.Errorf("local checkout origin must be a conventional github.com HTTPS or SSH URL")
	}
	name := strings.TrimSuffix(match[2], ".git")
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("local checkout origin has an invalid repository name")
	}
	return rm.addAt(origin, dest, true)
}

func (rm *RepoManager) validateLocalCheckout(dest string) error {
	if err := IsPrimaryGitCheckout(rm.git, dest); err != nil {
		return err
	}
	for _, check := range []struct{ flag, want string }{{"--is-bare-repository", "false"}, {"--show-toplevel", dest}} {
		out, _, code, err := rm.git.Run("git", "-C", dest, "rev-parse", check.flag)
		if err != nil || code != 0 {
			return fmt.Errorf("could not validate local checkout %s", check.flag)
		}
		got := strings.TrimSpace(out)
		if check.flag == "--show-toplevel" {
			got = filepath.Clean(got)
		}
		if got != check.want {
			return fmt.Errorf("local path must be the top level of a non-bare checkout")
		}
	}
	return nil
}
