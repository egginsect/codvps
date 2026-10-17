package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/egginsect/codvps/internal/fsutil"
)

// UpdateEntries holds an exclusive advisory lock across reading the current
// registry, calling fn, and atomically writing its returned entries. A callback
// error leaves the registry unchanged, as does a callback returning equal
// entries (nil and empty are equivalent). The callback must not call a registry
// writer recursively and should contain only short local work. Do not run
// clones, network access, other long-running Git operations, or head actions
// while holding the lock.
func UpdateEntries(path string, fn func([]Entry) ([]Entry, error)) error {
	if fn == nil {
		return fmt.Errorf("registry update callback cannot be nil")
	}
	return withRegistryLock(path, func() error {
		entries, err := ReadEntries(path)
		if err != nil {
			return err
		}
		original := append([]Entry(nil), entries...)
		updated, err := fn(entries)
		if err != nil {
			return err
		}
		if slices.Equal(original, updated) {
			return nil
		}
		return writeEntries(path, updated)
	})
}

// The lock lives beside the registry and is never replaced or unlinked. Locking
// the registry inode itself would allow another writer through after rename.
func withRegistryLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("failed to create registry directory: %w", err)
	}
	lockPath := path + ".lock"
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	lock, err := os.OpenFile(lockPath, flags|os.O_CREATE|os.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, os.ErrExist) {
		lock, err = os.OpenFile(lockPath, flags, 0)
	}
	if err != nil {
		return fmt.Errorf("failed to open registry lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if created {
		// The process umask may have removed owner permission bits.
		if err := lock.Chmod(0o600); err != nil {
			return fmt.Errorf("failed to set registry lock permissions: %w", err)
		}
	}
	if err := validateLockFile(lockPath, lock); err != nil {
		return err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("failed to acquire registry lock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	// A waiter must not proceed on an inode removed/replaced while it was
	// acquiring the lock, or it would no longer exclude the other writers.
	if err := validateLockFile(lockPath, lock); err != nil {
		return err
	}
	return fn()
}

func validateLockFile(path string, file *os.File) error {
	opened, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to inspect registry lock: %w", err)
	}
	if !opened.Mode().IsRegular() {
		return fmt.Errorf("registry lock must be a regular file")
	}
	if uid, _, ok := fsutil.OwnerIDs(opened); !ok || uid != os.Geteuid() {
		return fmt.Errorf("registry lock must be owned by the operator")
	}
	if opened.Mode()&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0o600 {
		return fmt.Errorf("registry lock must have mode 0600")
	}
	if stat, ok := opened.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		return fmt.Errorf("registry lock must have exactly one hard link")
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("failed to inspect registry lock path: %w", err)
	}
	if !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return fmt.Errorf("registry lock changed while being acquired")
	}
	return nil
}
