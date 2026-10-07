package shellenv

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeShell writes an executable script standing in for $SHELL. It is run
// as `<script> -lic <command>`, exactly like a login interactive shell, so
// $2 is the command the package asks it to run.
func fakeShell(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fakesh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// exported is a shell whose rc "files" print noise and export variables
// before running the command it was given.
const exported = `echo "welcome noise"
echo "more noise" >&2
export FOO=from-rc ANTHROPIC_API_KEY=rc-key CLAUDE_CODE_OAUTH_TOKEN=rc-token PATH=/rc/bin:/usr/bin:/bin
eval "$2"`

type execCall struct {
	path string
	argv []string
	env  []string
}

func options(shell string, base ...string) (Options, *bytes.Buffer, *execCall) {
	diag := &bytes.Buffer{}
	call := &execCall{}
	env := append([]string{"SHELL=" + shell, "UNITVAR=unit", "PATH=/usr/bin:/bin"}, base...)
	return Options{
		Environ:    func() []string { return env },
		PasswdPath: "/nonexistent/passwd",
		UID:        1000,
		Timeout:    5 * time.Second,
		Diag:       diag,
		Exec: func(path string, argv, env []string) error {
			*call = execCall{path, argv, env}
			return nil
		},
	}, diag, call
}

func TestCaptureReadsEnvironmentAfterMarkerIgnoringNoise(t *testing.T) {
	o, _, _ := options(fakeShell(t, exported))
	env, err := o.Capture()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FOO=from-rc", "UNITVAR=unit", "PATH=/rc/bin:/usr/bin:/bin"} {
		if !slices.Contains(env, want) {
			t.Errorf("captured env lacks %q: %q", want, env)
		}
	}
	for _, e := range env {
		if strings.Contains(e, "noise") {
			t.Errorf("rc noise leaked into the environment: %q", e)
		}
	}
}

func TestRunRemovesVariablesAfterTheShellSoRcCannotReAddThem(t *testing.T) {
	o, diag, call := options(fakeShell(t, exported))
	err := o.Run([]string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "UNITVAR"}, []string{"/bin/echo", "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if diag.Len() != 0 {
		t.Errorf("unexpected diagnostics: %q", diag)
	}
	if !slices.Contains(call.env, "FOO=from-rc") {
		t.Errorf("env lacks FOO: %q", call.env)
	}
	for _, e := range call.env {
		for _, gone := range []string{"ANTHROPIC_API_KEY=", "CLAUDE_CODE_OAUTH_TOKEN=", "UNITVAR="} {
			if strings.HasPrefix(e, gone) {
				t.Errorf("%s survived removal", gone)
			}
		}
	}
}

func TestRunPassesArgumentsIntactAndResolvesWithCapturedPath(t *testing.T) {
	bin := t.TempDir()
	tool := filepath.Join(bin, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The rc adds the tool's directory to PATH; the unit's PATH lacks it.
	o, _, call := options(fakeShell(t, `export PATH="`+bin+`:$PATH"; eval "$2"`))
	args := []string{"tool", "two words", `it's "quoted"`, "$HOME;`x`", "", "--flag=a b"}
	if err := o.Run(nil, args); err != nil {
		t.Fatal(err)
	}
	if call.path != tool {
		t.Errorf("resolved %q, want %q (captured PATH)", call.path, tool)
	}
	if !reflect.DeepEqual(call.argv, args) {
		t.Errorf("argv = %q, want %q", call.argv, args)
	}
}

func TestRunErrorsWhenTheCommandIsNotFound(t *testing.T) {
	o, _, call := options(fakeShell(t, exported))
	err := o.Run(nil, []string{"no-such-tool-xyz"})
	if err == nil || !strings.Contains(err.Error(), "no-such-tool-xyz not found") {
		t.Fatalf("err = %v", err)
	}
	if call.path != "" {
		t.Errorf("exec ran despite the failure")
	}
}

func assertFallback(t *testing.T, shell string, wantReason string) {
	t.Helper()
	o, diag, call := options(shell, "ANTHROPIC_API_KEY=unit-key")
	if err := o.Run([]string{"ANTHROPIC_API_KEY"}, []string{"/bin/echo", "x"}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(diag.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "codvps shell-exec: shell environment unavailable (") ||
		!strings.HasSuffix(lines[0], "); starting with the unit environment") || !strings.Contains(lines[0], wantReason) {
		t.Errorf("diagnostics = %q, want one fallback line mentioning %q", diag, wantReason)
	}
	if call.path != "/bin/echo" {
		t.Errorf("command did not start: %+v", call)
	}
	if !slices.Contains(call.env, "UNITVAR=unit") {
		t.Errorf("fallback env is not the unit environment: %q", call.env)
	}
	for _, e := range call.env {
		if strings.HasPrefix(e, "ANTHROPIC_API_KEY=") {
			t.Errorf("fallback kept a removed variable")
		}
	}
}

func TestShellTimeoutFallsBackToTheUnitEnvironment(t *testing.T) {
	shell := fakeShell(t, "exec sleep 30")
	o, diag, call := options(shell, "ANTHROPIC_API_KEY=unit-key")
	o.Timeout = 200 * time.Millisecond
	start := time.Now()
	if err := o.Run([]string{"ANTHROPIC_API_KEY"}, []string{"/bin/echo"}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("timeout was not enforced")
	}
	if !strings.Contains(diag.String(), "timed out") || strings.Count(diag.String(), "\n") != 1 {
		t.Errorf("diagnostics = %q", diag)
	}
	if !slices.Contains(call.env, "UNITVAR=unit") {
		t.Errorf("env = %q", call.env)
	}
}

func TestShellFailureFallsBackToTheUnitEnvironment(t *testing.T) {
	assertFallback(t, fakeShell(t, "exit 1"), "failed")
}

func TestShellWithoutMarkerFallsBackToTheUnitEnvironment(t *testing.T) {
	assertFallback(t, fakeShell(t, "echo no marker here"), "no environment marker")
}

func TestMissingShellFallsBackToTheUnitEnvironment(t *testing.T) {
	assertFallback(t, "/nonexistent/shell", "/nonexistent/shell")
}

func TestShellChoice(t *testing.T) {
	passwd := filepath.Join(t.TempDir(), "passwd")
	body := "root:x:0:0:root:/root:/bin/root-sh\noperator:x:1000:1000:Op,,,:/home/user:/usr/bin/opsh\nshort:x:1\n"
	if err := os.WriteFile(passwd, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{}
	o := Options{Environ: func() []string { return env }, PasswdPath: passwd, UID: 1000}
	if got := o.Shell(); got != "/usr/bin/opsh" {
		t.Errorf("passwd fallback = %q", got)
	}
	env = []string{"SHELL=/bin/zsh"}
	if got := o.Shell(); got != "/bin/zsh" {
		t.Errorf("$SHELL = %q", got)
	}
	env = nil
	o.UID = 4242
	if got := o.Shell(); got != "/bin/sh" {
		t.Errorf("no entry = %q", got)
	}
	o.PasswdPath = filepath.Join(t.TempDir(), "missing")
	if got := o.Shell(); got != "/bin/sh" {
		t.Errorf("no passwd file = %q", got)
	}
}

func TestPasswdShellIsUsedToCapture(t *testing.T) {
	sh := fakeShell(t, exported)
	passwd := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(passwd, []byte("operator:x:1000:1000::/home/user:"+sh+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, _, _ := options("")
	o.Environ = func() []string { return []string{"PATH=/usr/bin:/bin"} }
	o.PasswdPath = passwd
	env, err := o.Capture()
	if err != nil || !slices.Contains(env, "FOO=from-rc") {
		t.Fatalf("env = %q, err = %v", env, err)
	}
}

func TestRunRejectsAnEmptyCommand(t *testing.T) {
	o, _, _ := options(fakeShell(t, exported))
	if err := o.Run(nil, nil); err == nil {
		t.Fatal("want an error")
	}
}

func TestRemove(t *testing.T) {
	got := Remove([]string{"A=1", "B=2", "AB=3", "C"}, "A", "B")
	if !reflect.DeepEqual(got, []string{"AB=3", "C"}) {
		t.Errorf("Remove = %q", got)
	}
}

// A shell that prepends to the PATH it inherits, as rc files do, shows
// which starting environment the capture used.
func TestWithBaseStartsTheShellFromTheGivenEnvironment(t *testing.T) {
	sh := fakeShell(t, `PATH="/rc/bin:$PATH"; export PATH; eval "$2"`)
	o := Options{Environ: func() []string { return []string{"SHELL=" + sh, "PATH=/terminal/bin:/usr/bin:/bin"} }}

	for _, base := range []string{"/usr/bin:/bin", "/unit/bin:/usr/bin:/bin"} {
		env, err := o.WithBase([]string{"HOME=/h", "PATH=" + base}).Capture()
		if err != nil {
			t.Fatal(err)
		}
		if got, want := PathOf(env), "/rc/bin:"+base; got != want {
			t.Errorf("PATH = %q, want %q", got, want)
		}
		if envValue(env, "SHELL") != sh || envValue(env, "HOME") != "/h" {
			t.Errorf("SHELL and HOME must come from the base: %v", env)
		}
	}
}
