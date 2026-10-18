package repo

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
)

// CheckoutPath resolves only registered repositories. Legacy name-only
// records always refer to HOME/name, regardless of the current repository root.
// It does not require the checkout to exist; callers launching a process must
// additionally validate it with IsPrimaryGitCheckout.
func CheckoutPath(layout *paths.Layout, name string) (string, error) {
	if err := registry.ValidateName(name); err != nil {
		return "", err
	}
	entries, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil {
		return "", fmt.Errorf("failed to read registry: %w", err)
	}
	for _, entry := range entries {
		if entry.Name == name {
			dest := entryPath(layout, entry)
			if err := validateCheckoutPath(dest); err != nil {
				return "", err
			}
			if err := rejectSymlinkPath(dest); err != nil {
				return "", err
			}
			return dest, nil
		}
	}
	return "", fmt.Errorf("repository %q is not registered", name)
}

func entryPath(layout *paths.Layout, entry registry.Entry) string {
	if entry.Path != "" {
		return entry.Path
	}
	return filepath.Join(layout.Home(), entry.Name)
}

func validateCheckoutPath(dest string) error {
	if !filepath.IsAbs(dest) || filepath.Clean(dest) != dest || dest == string(filepath.Separator) {
		return fmt.Errorf("checkout destination must be a clean absolute path other than the filesystem root: %q", dest)
	}
	if !utf8.ValidString(dest) || strings.IndexFunc(dest, unicode.IsControl) >= 0 {
		return fmt.Errorf("checkout destination contains invalid UTF-8 or control characters: %q", dest)
	}
	return nil
}

func rejectSymlinkPath(dest string) error {
	if err := validateCheckoutPath(dest); err != nil {
		return err
	}
	symlinked, err := hasSymlinkAncestor(string(filepath.Separator), dest)
	if err != nil {
		return fmt.Errorf("failed to inspect repository path %q: %w", dest, err)
	}
	if symlinked {
		return fmt.Errorf("repository path %q has a symlinked component and cannot be managed", dest)
	}
	return nil
}

// hasSymlinkAncestor deliberately starts at the filesystem root, even when
// a caller supplies a narrower root. Checking only HOME misses symlinked
// parents of HOME and configured roots outside it.
func hasSymlinkAncestor(_ string, target string) (bool, error) {
	if !filepath.IsAbs(target) {
		return false, fmt.Errorf("path is not absolute: %q", target)
	}
	cur := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(target, cur), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
		if cur != target && !fi.IsDir() {
			return false, fmt.Errorf("path component %q is not a directory", cur)
		}
	}
	return false, nil
}

// A missing parent is an unavailable root or ancestor, not evidence that a
// particular checkout was removed. Never prune (or recreate) it implicitly.
func requireCheckoutParent(dest string) error {
	parent := filepath.Dir(dest)
	fi, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("checkout parent %q is unavailable; keeping registration: %w", parent, err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("checkout parent %q is not a real directory", parent)
	}
	return nil
}
