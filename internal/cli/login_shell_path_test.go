package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// The login-shell PATH is captured from the base environment it is given,
// not from doctor's own: a fake rc prepends to whatever PATH it inherits.
func TestLoginShellPathStartsFromTheGivenBaseEnvironment(t *testing.T) {
	sh := filepath.Join(t.TempDir(), "fakesh")
	if err := os.WriteFile(sh, []byte("#!/bin/sh\nPATH=\"/rc/bin:$PATH\"; export PATH; eval \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/terminal/only:/usr/bin:/bin")

	got, err := loginShellPath([]string{"HOME=/h", "PATH=/usr/local/bin:/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "/rc/bin:/usr/local/bin:/usr/bin:/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}
