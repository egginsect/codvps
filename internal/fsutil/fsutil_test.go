package fsutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "test.txt")
	data := []byte("test content")

	err := AtomicWrite(path, data, 0600)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	// Verify file exists and has correct content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !bytes.Equal(content, data) {
		t.Errorf("content = %q, want %q", string(content), string(data))
	}

	// Verify permissions
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if stat.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", stat.Mode().Perm())
	}
}

func TestAtomicWriteOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")

	// Write initial content
	if err := AtomicWrite(path, []byte("initial"), 0600); err != nil {
		t.Fatalf("first write error = %v", err)
	}

	// Overwrite with new content
	newData := []byte("updated content")
	if err := AtomicWrite(path, newData, 0644); err != nil {
		t.Fatalf("second write error = %v", err)
	}

	// Verify new content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !bytes.Equal(content, newData) {
		t.Errorf("content = %q, want %q", string(content), string(newData))
	}

	// Verify new permissions
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if stat.Mode().Perm() != 0644 {
		t.Errorf("mode = %o, want 0644", stat.Mode().Perm())
	}
}

func TestAtomicWriteReader(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	data := []byte("test content from reader")

	reader := bytes.NewReader(data)
	err := AtomicWriteReader(path, reader, 0600)
	if err != nil {
		t.Fatalf("AtomicWriteReader() error = %v", err)
	}

	// Verify file exists and has correct content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if !bytes.Equal(content, data) {
		t.Errorf("content = %q, want %q", string(content), string(data))
	}

	// Verify permissions
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if stat.Mode().Perm() != 0600 {
		t.Errorf("mode = %o, want 0600", stat.Mode().Perm())
	}
}

func TestAtomicWriteCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "deep", "nested", "dir", "test.txt")

	err := AtomicWrite(path, []byte("test"), 0600)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	// Verify file exists
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if stat.IsDir() {
		t.Errorf("expected file, got directory")
	}
}

func TestMkdirAll(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "a", "b", "c")

	err := MkdirAll(path, 0700)
	if err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if !stat.IsDir() {
		t.Errorf("expected directory, got file")
	}
}

func TestAtomicWriteEmptyData(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "empty.txt")

	err := AtomicWrite(path, []byte{}, 0600)
	if err != nil {
		t.Fatalf("AtomicWrite() error = %v", err)
	}

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if stat.Size() != 0 {
		t.Errorf("size = %d, want 0", stat.Size())
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestAtomicWriteReaderFailureLeavesNoTempFileAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWriteReader(path, failingReader{}, 0o600); err == nil {
		t.Fatal("expected an error from a failing reader")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "target" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp file left behind: %v", names)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("target changed on failure: %q", got)
	}
}
