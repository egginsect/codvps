package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// /dev/null is a character device but not a terminal: a run with stdin
// redirected from it must never be treated as interactive.
func TestIsTerminalRejectsDevNullAndRegularFiles(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	if IsTerminal(devNull) {
		t.Fatal("/dev/null was reported as a terminal")
	}

	regular, err := os.Create(filepath.Join(t.TempDir(), "file"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regular.Close() }()
	if IsTerminal(regular) {
		t.Fatal("a regular file was reported as a terminal")
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if IsTerminal(r) {
		t.Fatal("a pipe was reported as a terminal")
	}

	if IsTerminal(nil) {
		t.Fatal("a nil file was reported as a terminal")
	}
}
