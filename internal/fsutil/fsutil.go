// Package fsutil provides filesystem utilities.
package fsutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AtomicWrite writes data to path atomically: it writes a temporary file in
// the same directory, syncs it, sets mode, renames it over path and syncs the
// directory. On any failure the temporary file is removed and path is left
// untouched.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return AtomicWriteReader(path, bytes.NewReader(data), mode)
}

// AtomicWriteReader is AtomicWrite with the contents read from reader.
func AtomicWriteReader(path string, reader io.Reader, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	closed := false
	defer func() {
		if err == nil {
			return
		}
		if !closed {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if _, err = io.Copy(tmpFile, reader); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err = tmpFile.Chmod(mode); err != nil {
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}
	if err = tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	closed = true
	if err = tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to rename %q to %q: %w", tmpPath, path, err)
	}
	return syncDir(dir)
}

// syncDir makes a completed rename durable by syncing its directory.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("failed to open directory %q: %w", dir, err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return fmt.Errorf("failed to sync directory %q: %w", dir, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("failed to close directory %q: %w", dir, closeErr)
	}
	return nil
}

// MkdirAll creates a directory with the given mode.
func MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
