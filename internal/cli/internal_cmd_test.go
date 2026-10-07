package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInternalCommandUsage(t *testing.T) {
	cmd := &internalCmd{}

	if err := cmd.Execute(nil); err != nil {
		if _, ok := err.(*InvalidUsageError); !ok {
			t.Errorf("internal with no args should be InvalidUsageError, got %T", err)
		}
	} else {
		t.Errorf("internal with no args should return an error")
	}
	if err := cmd.Execute([]string{"unknown"}); err == nil {
		t.Errorf("internal unknown should be an error")
	}
	if err := cmd.Execute([]string{"codex-remote-start", "extra"}); err == nil {
		t.Errorf("internal codex-remote-start takes no arguments")
	}
}

// The root-side sandbox helper was removed: it is no longer an
// internal command, so a stale unit or script naming it fails loudly.
func TestInternalCodexIsolationIsGone(t *testing.T) {
	for _, args := range [][]string{{"codex-isolation"}, {"codex-isolation", "validate", "operator"}, {"codex-isolation", "sync", "operator"}} {
		if err := (&internalCmd{}).Execute(args); err == nil {
			t.Errorf("internal %v was accepted", args)
		}
	}
}

// The ExecStart wiring fails with a descriptive exit error when the unit
// environment names no usable Codex (here: an empty HOME/CODEX_HOME with
// no pinned binary), without running anything.
func TestInternalCodexRemoteStartReportsMissingExecutable(t *testing.T) {
	t.Setenv("CODVPS_CODEX_BIN", "")
	// A fake login shell: never the developer's real one and rc files.
	fake := filepath.Join(t.TempDir(), "fakesh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\neval \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", fake)
	t.Setenv("CODEX_HOME", t.TempDir())
	err := (&internalCmd{}).Execute([]string{"codex-remote-start"})
	var exitErr interface{ ExitCode() int }
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(err.Error(), "Codex Remote executable is missing or not executable") {
		t.Fatalf("codex-remote-start = %v, want exit 1 naming the missing executable", err)
	}
}
