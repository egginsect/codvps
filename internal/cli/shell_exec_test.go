package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/shellenv"
)

// shellExecOptions is a shell environment backed by a fake $SHELL that
// exports the Claude credentials from its "rc", and records what would be
// exec'd.
func shellExecOptions(t *testing.T) (shellenv.Options, *[]string, *[]string) {
	t.Helper()
	sh := filepath.Join(t.TempDir(), "fakesh")
	script := "#!/bin/sh\nexport FOO=bar ANTHROPIC_API_KEY=k EXTRA=x ANTHROPIC_BASE_URL=u\neval \"$2\"\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var argv, env []string
	return shellenv.Options{
		Environ:    func() []string { return []string{"SHELL=" + sh, "PATH=/usr/bin:/bin"} },
		PasswdPath: "/nonexistent",
		Timeout:    5 * time.Second,
		Diag:       &strings.Builder{},
		Exec: func(_ string, a, e []string) error {
			argv, env = a, e
			return nil
		},
	}, &argv, &env
}

func TestShellExecAppliesProviderAndUnsetRemovals(t *testing.T) {
	opts, argv, env := shellExecOptions(t)
	err := internalShellExec([]string{"--provider", "claude", "--unset", "EXTRA", "--", "/bin/echo", "a b", "--provider"}, providers.All(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/bin/echo", "a b", "--provider"}; !reflect.DeepEqual(*argv, want) {
		t.Errorf("argv = %q, want %q", *argv, want)
	}
	if !slices.Contains(*env, "FOO=bar") {
		t.Errorf("env lacks FOO: %q", *env)
	}
	for _, e := range *env {
		for _, gone := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_BASE_URL=", "CLAUDE_CODE_OAUTH_TOKEN=", "EXTRA="} {
			if strings.HasPrefix(e, gone) {
				t.Errorf("%s was not removed", gone)
			}
		}
	}
}

func TestShellExecWithoutProviderKeepsEverythingButUnset(t *testing.T) {
	opts, _, env := shellExecOptions(t)
	if err := internalShellExec([]string{"--", "/bin/echo"}, providers.All(), opts); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*env, "ANTHROPIC_API_KEY=k") {
		t.Errorf("env = %q", *env)
	}
}

func TestShellExecUnknownProviderIsAnError(t *testing.T) {
	opts, argv, _ := shellExecOptions(t)
	err := internalShellExec([]string{"--provider", "nope", "--", "/bin/echo"}, providers.All(), opts)
	if err == nil || !strings.Contains(err.Error(), `unknown provider "nope"`) {
		t.Fatalf("err = %v", err)
	}
	if *argv != nil {
		t.Errorf("command ran")
	}
}

func TestShellExecUsageErrors(t *testing.T) {
	opts, _, _ := shellExecOptions(t)
	for _, args := range [][]string{
		nil,
		{"--"},
		{"/bin/echo"},
		{"--provider"},
		{"--provider", "claude"},
		{"--unset", "X", "/bin/echo"},
	} {
		err := internalShellExec(args, providers.All(), opts)
		var usage *InvalidUsageError
		if !errors.As(err, &usage) {
			t.Errorf("%q: err = %v, want usage error", args, err)
		}
	}
}
