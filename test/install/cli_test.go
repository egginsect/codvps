//go:build linux

package install_test

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Keep the hermetic CLI installer suite in the ordinary Go test entry point.
// It installs only fixture files in temporary directories, never the host CLI.
func TestCLIInstaller(t *testing.T) {
	// Subprocess file reads are invisible to Go's test cache. Register both
	// script inputs here so editing either one invalidates a cached test result.
	for _, path := range []string{"cli_test.sh", "../../tools/install-cli.sh"} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("read installer test input %s: %v", path, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "cli_test.sh")
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("CLI installer fixtures failed: %v\n%s", err, output)
	}
}

// Exercise the source-checkout entry point without a real Go build, downloads,
// privileged commands, or writes outside the test's temporary directories.
func TestSourceInstaller(t *testing.T) {
	// The shell fixture copies these inputs to a fresh checkout. Register every
	// copied file, including helpers and the Makefile, with Go's test cache:
	// subprocess reads alone do not invalidate a previously cached success.
	for _, path := range []string{
		"source_test.sh", "../../install.sh", "../../Makefile", "../../go.mod",
	} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("read source installer test input %s: %v", path, err)
		}
	}
	if _, err := os.ReadFile("../../go.sum"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("read optional go.sum: %v", err)
	}
	for _, root := range []string{"../../tools", "../../cmd", "../../internal"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			_, err = os.ReadFile(path)
			return err
		})
		if err != nil {
			t.Fatalf("read source installer test inputs under %s: %v", root, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "source_test.sh")
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("source installer fixtures failed: %v\n%s", err, output)
	}
}
