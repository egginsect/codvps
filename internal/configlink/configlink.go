// Package configlink keeps an operator's agent configuration in a git
// repository they own: `codvps config link <repo-url>` registers the repo
// like `repo add`, then replaces each mirrored file under home with a
// symlink into the checkout. Git is the history: updates are a pull, and an
// edit made under home is an edit to the checkout.
package configlink

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Layout is where each top-level directory of the repo is linked under
// home. Files are linked one by one, never whole directories, so the
// credentials and state that live next to them stay untouched.
var Layout = []struct {
	Dir   string
	Homes []string
}{
	{"claude", []string{".claude"}},
	{"codex", []string{".codex"}},
	{"agents", []string{".agents"}},
	{"opencode", []string{filepath.Join(".config", "opencode")}},
	{"cursor", []string{".cursor"}},
}

// refusedNames are credentials and per-host state a config repo must not
// carry; refusedDirs are state directories anywhere inside a mapped dir.
var (
	refusedNames = map[string]bool{".credentials.json": true, "auth.json": true, "hosts.yml": true, "history.jsonl": true}
	refusedDirs  = map[string]bool{"sessions": true, "projects": true, "log": true, "logs": true}
)

// RepoFile is where the linked checkout's absolute path is recorded.
func RepoFile(configDir string) string { return filepath.Join(configDir, "config-repo") }

// Pair is one repo file and the home path linked to it.
type Pair struct {
	Source string // absolute path in the checkout
	Target string // absolute path under home
}

// Refusal is a path config link will not touch, and why.
type Refusal struct {
	Path   string
	Reason string
}

// Pairs lists every mirrored file of checkout with its home target, and the
// repo files refused as credentials or state.
func Pairs(home, checkout string) ([]Pair, []Refusal, error) {
	var pairs []Pair
	var refused []Refusal
	for _, m := range Layout {
		root := filepath.Join(checkout, m.Dir)
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if reason := refusal(rel); reason != "" {
				refused = append(refused, Refusal{Path: path, Reason: reason})
				return nil
			}
			for _, h := range m.Homes {
				pairs = append(pairs, Pair{Source: path, Target: filepath.Join(home, h, rel)})
			}
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("could not read %s: %w", root, err)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Target < pairs[j].Target })
	return pairs, refused, nil
}

func refusal(rel string) string {
	parts := strings.Split(rel, string(filepath.Separator))
	name := parts[len(parts)-1]
	if refusedNames[name] || strings.Contains(name, ".sqlite") {
		return "credentials or per-host state never belong in a config repo"
	}
	for _, p := range parts[:len(parts)-1] {
		if refusedDirs[p] {
			return "per-host state directory " + p + "/ never belongs in a config repo"
		}
	}
	return ""
}

// Result is what Link did.
type Result struct {
	Linked, AlreadyLinked []string
	BackedUp              map[string]string // home path -> backup path
	Refused               []Refusal
}

// Link links every mirrored file of checkout into home and records the
// checkout in configDir. A home file that differs from the repo's is moved
// to configDir/config-link-backup first; a home path that is a directory or
// a symlink elsewhere is refused and left alone. Linking twice is a no-op.
func Link(home, configDir, checkout string) (Result, error) {
	res := Result{BackedUp: map[string]string{}}
	pairs, refused, err := Pairs(home, checkout)
	if err != nil {
		return res, err
	}
	res.Refused = refused
	for _, p := range pairs {
		fi, err := os.Lstat(p.Target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return res, fmt.Errorf("could not inspect %s: %w", p.Target, err)
		case fi.Mode()&fs.ModeSymlink != 0:
			if dest, _ := os.Readlink(p.Target); dest == p.Source {
				res.AlreadyLinked = append(res.AlreadyLinked, p.Target)
				continue
			}
			res.Refused = append(res.Refused, Refusal{Path: p.Target, Reason: "is a symlink to somewhere else"})
			continue
		case !fi.Mode().IsRegular():
			res.Refused = append(res.Refused, Refusal{Path: p.Target, Reason: "is not a regular file"})
			continue
		default:
			same, err := sameContent(p.Source, p.Target)
			if err != nil {
				return res, err
			}
			if same {
				if err := os.Remove(p.Target); err != nil {
					return res, err
				}
			} else {
				rel, err := filepath.Rel(home, p.Target)
				if err != nil {
					return res, err
				}
				backup := filepath.Join(configDir, "config-link-backup", rel)
				if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
					return res, err
				}
				if err := os.Rename(p.Target, backup); err != nil {
					return res, fmt.Errorf("could not back up %s: %w", p.Target, err)
				}
				res.BackedUp[p.Target] = backup
			}
		}
		if err := os.MkdirAll(filepath.Dir(p.Target), 0o700); err != nil {
			return res, err
		}
		if err := os.Symlink(p.Source, p.Target); err != nil {
			return res, fmt.Errorf("could not link %s: %w", p.Target, err)
		}
		res.Linked = append(res.Linked, p.Target)
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return res, err
	}
	if err := os.WriteFile(RepoFile(configDir), []byte(checkout+"\n"), 0o600); err != nil {
		return res, err
	}
	return res, nil
}

func sameContent(a, b string) (bool, error) {
	da, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	db, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(da, db), nil
}

// Linked is the checkout recorded by Link, or "" when none is linked.
func Linked(configDir string) (string, error) {
	data, err := os.ReadFile(RepoFile(configDir))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Unlink replaces every home link into the linked checkout with a regular
// copy of the file it points to (mode kept) and forgets the checkout. It
// returns how many links it replaced; with nothing linked it does nothing.
func Unlink(home, configDir string) (int, error) {
	checkout, err := Linked(configDir)
	if err != nil || checkout == "" {
		return 0, err
	}
	pairs, _, err := Pairs(home, checkout)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pairs {
		if dest, err := os.Readlink(p.Target); err != nil || dest != p.Source {
			continue
		}
		if err := copyOver(p.Source, p.Target); err != nil {
			return n, err
		}
		n++
	}
	return n, os.Remove(RepoFile(configDir))
}

// copyOver writes src's content and mode to a temp file next to dst and
// renames it over dst, so dst is never missing.
func copyOver(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".codvps-unlink"
	if err := os.WriteFile(tmp, data, fi.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// Drift reports mirrored home paths that are no longer links into the
// checkout: replaced by a regular file (a tool rewrote it) or missing (the
// repo gained a file since the last link).
func Drift(home, checkout string) (replaced, missing []string, err error) {
	pairs, _, err := Pairs(home, checkout)
	if err != nil {
		return nil, nil, err
	}
	for _, p := range pairs {
		fi, err := os.Lstat(p.Target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, p.Target)
		case err != nil:
			return nil, nil, err
		case fi.Mode()&fs.ModeSymlink == 0:
			replaced = append(replaced, p.Target)
		}
	}
	return replaced, missing, nil
}
