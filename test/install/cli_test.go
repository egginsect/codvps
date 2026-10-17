//go:build linux

package install_test

import (
	"context"
	"os"
	"os/exec"
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
