package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// OwnerIDs returns a file's owning uid and gid; ok is false when the
// platform does not report them.
func OwnerIDs(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// EnsurePrivateDir creates dir (and missing parents) and forces it to a
// real, caller-owned, mode-0700 directory; an existing symlink or
// non-directory is refused, never followed.
func EnsurePrivateDir(dir string) error {
	if fi, err := os.Lstat(dir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return fmt.Errorf("%s must be a real directory", dir)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to inspect %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(dir), err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%s must be a real directory", dir)
	}
	if uid, _, ok := OwnerIDs(fi); !ok || uid != os.Geteuid() {
		return fmt.Errorf("%s is not owned by the operator", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("failed to set the mode of %s: %w", dir, err)
	}
	return nil
}
