// Package userinstall installs native provider CLIs without host provisioning.
package userinstall

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/runner"
)

const Usage = "codvps install [--user] [--components claude,codex,cursor] [--dry-run]"

type provider struct{ name, binary, url, shell string }

var providers = []provider{
	{"claude", ".local/bin/claude", "https://claude.ai/install.sh", "bash"},
	{"codex", ".codex/packages/standalone/current/codex", "https://chatgpt.com/codex/install.sh", "sh"},
	{"cursor", ".local/bin/agent", "https://cursor.com/install", "bash"},
}

// Options keeps all subprocesses injectable for hermetic tests.
type Options struct {
	Home      string
	EUID      int
	Runner    runner.Runner
	Out, Diag io.Writer
}

func selection(args []string) ([]provider, bool, error) {
	names := "claude,codex,cursor"
	dry, seen := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--user":
		case a == "--dry-run":
			dry = true
		case a == "--components" || strings.HasPrefix(a, "--components="):
			if seen {
				return nil, false, fmt.Errorf("duplicate --components")
			}
			seen = true
			if a == "--components" {
				i++
				if i == len(args) {
					return nil, false, fmt.Errorf("--components requires a value")
				}
				names = args[i]
			} else {
				names = strings.TrimPrefix(a, "--components=")
			}
		default:
			return nil, false, fmt.Errorf("unknown user install argument %q (usage: %s)", a, Usage)
		}
	}
	result := []provider{}
	used := map[string]bool{}
	for _, n := range strings.Split(names, ",") {
		found := false
		for _, p := range providers {
			if p.name == n {
				if used[n] {
					return nil, false, fmt.Errorf("duplicate component %q", n)
				}
				used[n] = true
				result = append(result, p)
				found = true
				break
			}
		}
		if !found {
			return nil, false, fmt.Errorf("unsupported user component %q (choose claude,codex,cursor)", n)
		}
	}
	return result, dry, nil
}

// checkPath refuses paths redirected outside the user's physical home.
func checkPath(home, path string) error {
	for p := path; p != home; p = filepath.Dir(p) {
		if p == filepath.Dir(p) {
			return fmt.Errorf("path outside home: %s", path)
		}
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(home, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refusing path outside home: %s", p)
		}
	}
	return nil
}

// Install only manages user-native CLIs. It never changes settings, credentials,
// service state, or the system component registry. Existing binaries are kept.
func Install(o Options, args []string) error {
	selected, dry, err := selection(args)
	if err != nil {
		return err
	}
	if o.EUID == 0 {
		return fmt.Errorf("--user must run as a non-root user, without sudo")
	}
	if o.Runner == nil || o.Out == nil || o.Diag == nil {
		return fmt.Errorf("user install requires runner and output writers")
	}
	if !filepath.IsAbs(o.Home) {
		return fmt.Errorf("HOME must be absolute")
	}
	home, err := filepath.EvalSymlinks(o.Home)
	if err != nil {
		return err
	}
	if home != filepath.Clean(o.Home) {
		return fmt.Errorf("HOME must not be a symlink")
	}
	for _, dir := range []string{".local/bin", ".local/share", ".claude", ".codex", ".cursor", ".bashrc", ".bash_profile", ".zshrc", ".zprofile", ".profile"} {
		if err := checkPath(home, filepath.Join(home, dir)); err != nil {
			return err
		}
	}
	// Check existing installer-owned trees without reading file contents. Native
	// installers may replace version directories before creating the binary link.
	for _, rel := range []string{".local/share/cursor-agent", ".local/share/claude", ".claude/downloads", ".codex/packages/standalone"} {
		root := filepath.Join(home, rel)
		if err := checkPath(home, root); err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := checkPath(home, path); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				target, err := os.Stat(path)
				if err != nil {
					return err
				}
				if target.IsDir() {
					// The official Codex selection link points into the traversed releases
					// directory. Other directory links could hide unvisited descendants.
					allowed := filepath.Join(home, ".codex/packages/standalone/current")
					physical, err := filepath.EvalSymlinks(path)
					if err != nil {
						return err
					}
					releaseRoot := filepath.Join(home, ".codex/packages/standalone/releases")
					rel, err := filepath.Rel(releaseRoot, physical)
					if path != allowed || err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
						return fmt.Errorf("refusing symlinked installer storage directory: %s", path)
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Preflight every destination before any installer is launched.
	for _, p := range selected {
		if err := checkPath(home, filepath.Join(home, p.binary)); err != nil {
			return err
		}
	}
	for _, p := range selected {
		bin := filepath.Join(home, p.binary)
		fi, statErr := os.Stat(bin)
		if statErr == nil {
			if !fi.Mode().IsRegular() || fi.Mode().Perm()&0111 == 0 {
				return fmt.Errorf("existing %s is not an executable regular file", bin)
			}
			if dry {
				if _, err := fmt.Fprintf(o.Out, "%s: keep %s\n", p.name, bin); err != nil {
					return err
				}
				continue
			}
			version, stderr, code, err := o.Runner.Run(bin, "--version")
			if err != nil || code != 0 {
				return fmt.Errorf("existing %s failed --version; left unchanged: %s", p.name, strings.TrimSpace(stderr))
			}
			if _, err := fmt.Fprintf(o.Out, "%s: already installed at %s (%s); kept unchanged\n", p.name, bin, strings.TrimSpace(version)); err != nil {
				return err
			}
			continue
		}
		if !os.IsNotExist(statErr) {
			return statErr
		}
		if p.name == "cursor" || p.name == "codex" {
			aliasName := "cursor-agent"
			if p.name == "codex" {
				aliasName = "codex"
			}
			alias := filepath.Join(home, ".local/bin", aliasName)
			if _, err := os.Lstat(alias); err == nil {
				return fmt.Errorf("existing %s without expected native package; repair the native installation before retrying", alias)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if _, err := fmt.Fprintf(o.Out, "%s: install for current user from %s\n", p.name, p.url); err != nil {
			return err
		}
		if dry {
			continue
		}
		if err := installOne(o, p); err != nil {
			return err
		}
		if err := checkPath(home, bin); err != nil {
			return err
		}
		fi, err = os.Stat(bin)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("%s installer did not create expected native executable %s", p.name, bin)
		}
		version, stderr, code, err := o.Runner.Run(bin, "--version")
		if err != nil || code != 0 {
			return fmt.Errorf("%s verification failed: %s", p.name, strings.TrimSpace(stderr))
		}
		if _, err := fmt.Fprintf(o.Out, "%s: installed %s (%s)\n", p.name, bin, strings.TrimSpace(version)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(o.Out, "User CLI installation checked. No login, remote access, system services or host provisioning were enabled."); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(o.Out, "For hosts without systemd, configure separately: codvps config set session_mode manual"); err != nil {
		return err
	}
	return nil
}

func installOne(o Options, p provider) error {
	dir, err := os.MkdirTemp("", "codvps-user-install-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	script := filepath.Join(dir, "install.sh")
	_, stderr, code, err := o.Runner.Run("curl", "--disable", "--fail", "--silent", "--show-error", "--location", "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "15", "--max-time", "120", "--output", script, p.url)
	if err != nil || code != 0 {
		return fmt.Errorf("download %s installer failed: %s", p.name, strings.TrimSpace(stderr))
	}
	code, err = o.Runner.RunWithIO(p.shell, []string{script}, nil, o.Out, o.Diag)
	if err != nil || code != 0 {
		return fmt.Errorf("%s installer failed (exit %d): %v", p.name, code, err)
	}
	return nil
}
