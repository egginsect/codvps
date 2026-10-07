package login

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

func stdoutOf(ctx *Context) string { return ctx.Stdout.(*strings.Builder).String() }
func stderrOf(ctx *Context) string { return ctx.Stderr.(*strings.Builder).String() }

// Without a terminal a logged-out run starts GitHub's device flow, and the
// one-time code and URL reach the caller's stderr while gh waits.
func TestGitHub_NoTTY_LoggedOut_StartsDeviceFlow(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context() // IsTTY is false

	status, detail, err := ensureAuth(ctx, false)
	if err != nil || status != StatusOK {
		t.Fatalf("status=%q detail=%q err=%v, want ok", status, detail, err)
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 1 {
		t.Fatalf("gh auth login calls = %d, want 1", got)
	}
	if !strings.Contains(stderrOf(ctx), "ABCD-1234") || !strings.Contains(stderrOf(ctx), "github.com/login/device") {
		t.Fatalf("device code and URL not shown: %q", stderrOf(ctx))
	}
}

// --non-interactive still refuses to start it.
func TestGitHub_NoTTY_ExplicitNonInteractive_StillExit2(t *testing.T) {
	env := newTestEnv(t)
	code, err := GitHub(env.Context(), true, "", false, "", "", false, false)
	if code != 2 || !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("code=%d err=%v, want 2 / ErrAuthRequired", code, err)
	}
}

// The whole flow with no terminal and no --key: the SSH key prompt takes
// its default (generate), says so, and nothing reads stdin.
func TestGitHub_NoTTY_PromptsTakeDefaults(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.Stdin = failReader{t}

	code, err := GitHub(ctx, false, "", true, "Op", "op@example.com", true, false)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s\n%s", code, err, stdoutOf(ctx), stderrOf(ctx))
	}
	if !strings.Contains(stdoutOf(ctx), "assuming yes: generate SSH key at "+env.keyPath()+" (--yes)") {
		t.Errorf("auto-answer not reported:\n%s", stdoutOf(ctx))
	}
	if _, statErr := os.Stat(env.keyPath()); statErr != nil {
		t.Errorf("key was not generated: %v", statErr)
	}
	if got := env.countCalls(t, "gh", "auth", "login"); got != 1 {
		t.Errorf("device flow calls = %d, want 1", got)
	}
}

// Without --yes the reason given is the missing terminal.
func TestGitHub_NoTTY_ReasonIsNoTerminal(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	if _, status, _, err := ensureSSHKey(ctx, false, "", true); err != nil || status != StatusOK {
		t.Fatalf("status=%q err=%v", status, err)
	}
	if !strings.Contains(stdoutOf(ctx), "assuming yes: generate SSH key at "+env.keyPath()+" (no terminal)") {
		t.Errorf("auto-answer not reported:\n%s", stdoutOf(ctx))
	}
}

// An empty passphrase is the non-default: without a terminal and without
// --passphrase-empty the key is not generated.
func TestGitHub_NoTTY_NeverAssumesEmptyPassphrase(t *testing.T) {
	env := newTestEnv(t)
	env.setLoggedIn()
	ctx := env.Context()

	_, status, detail, err := ensureSSHKey(ctx, false, "", false)
	if err != nil || status != StatusPending || !strings.Contains(detail, "--passphrase-empty") {
		t.Fatalf("status=%q detail=%q err=%v, want pending naming --passphrase-empty", status, detail, err)
	}
	if got := env.countCalls(t, "ssh-keygen"); got != 0 {
		t.Fatalf("ssh-keygen ran %d times", got)
	}
}

// --yes on a terminal accepts the defaults without reading stdin.
func TestGitHub_Yes_OnTTY_AcceptsDefaults(t *testing.T) {
	env := newTestEnv(t)
	env.setLoggedIn()
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	ctx.AssumeYes = true
	ctx.Stdin = failReader{t}

	path, status, _, err := ensureSSHKey(ctx, false, "", true)
	if err != nil || status != StatusOK || path != env.keyPath() {
		t.Fatalf("path=%q status=%q err=%v", path, status, err)
	}
	if !strings.Contains(stdoutOf(ctx), "assuming yes: generate SSH key at "+env.keyPath()+" (--yes)") {
		t.Errorf("auto-answer not reported:\n%s", stdoutOf(ctx))
	}
}

// On a terminal without --yes the prompt is still asked.
func TestGitHub_TTY_WithoutYes_StillPrompts(t *testing.T) {
	env := newTestEnv(t)
	ctx := env.Context()
	ctx.IsTTY = func() bool { return true }
	ctx.Stdin = strings.NewReader("n\n")

	_, status, _, err := ensureSSHKey(ctx, false, "", false)
	if err != nil || status != StatusSkipped || !strings.Contains(stdoutOf(ctx), "[Y/n]") {
		t.Fatalf("status=%q err=%v out=%q, want the prompt answered n", status, err, stdoutOf(ctx))
	}
}

type failReader struct{ t *testing.T }

func (r failReader) Read([]byte) (int, error) {
	r.t.Helper()
	r.t.Error("stdin was read without a terminal")
	return 0, io.EOF
}

// installRunner models a host whose gh is missing or too old: probing gh
// reports that until the apt install runs, after which the sandbox's fake
// gh answers. Everything else on PATH is the real ExecRunner reaching only
// the sandbox's fakes; sudo, curl and apt are scripted here.
type installRunner struct {
	real      *runner.ExecRunner
	missing   bool
	tooOld    bool
	sudoPass  bool // sudo -n true fails: a password would be asked
	installed bool
	lines     []string
	stdin     map[string]string
}

func (r *installRunner) Run(name string, args ...string) (string, string, int, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.lines = append(r.lines, line)
	switch {
	case name == "gh" && !r.installed && r.missing:
		return "", "", 1, &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	case name == "gh" && !r.installed && r.tooOld:
		return "", "unknown flag: --json\n", 1, nil
	case name == "sudo" && line == "sudo -n true":
		if r.sudoPass {
			return "", "sudo: a password is required\n", 1, nil
		}
		return "", "", 0, nil
	case name == "dpkg":
		return "amd64\n", "", 0, nil
	}
	return r.real.Run(name, args...)
}

func (r *installRunner) RunWithIO(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	r.lines = append(r.lines, line)
	switch name {
	case "curl":
		for i, a := range args {
			if a == "-o" {
				_ = os.WriteFile(args[i+1], []byte("keyring"), 0o600)
			}
		}
		return 0, nil
	case "sudo", "install", "env":
		if stdin != nil {
			data, _ := io.ReadAll(stdin)
			if r.stdin == nil {
				r.stdin = map[string]string{}
			}
			r.stdin[line] = string(data)
		}
		if strings.HasSuffix(line, "apt-get install -y gh") {
			r.installed = true
		}
		return 0, nil
	}
	return r.real.RunWithIO(name, args, stdin, stdout, stderr)
}

func (r *installRunner) ran(sub string) bool {
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func installCtx(t *testing.T, r *installRunner) (*testEnv, *Context) {
	env := newTestEnv(t)
	r.real = runner.NewExecRunner()
	ctx := env.Context()
	ctx.Runner = r
	ctx.SysRoot = filepath.Join(t.TempDir(), "sys")
	return env, ctx
}

func TestEnsureGH_Missing_InstallsOnDemandThroughSudo(t *testing.T) {
	r := &installRunner{missing: true}
	_, ctx := installCtx(t, r)

	if err := ensureGH(ctx); err != nil {
		t.Fatalf("ensureGH: %v\n%s", err, stdoutOf(ctx))
	}
	for _, want := range []string{
		"sudo env DEBIAN_FRONTEND=noninteractive apt-get update",
		"sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y gh",
		"sudo install -D -m 0644 /dev/stdin /etc/apt/keyrings/githubcli-archive-keyring.gpg",
		"sudo install -D -m 0644 /dev/stdin /etc/apt/sources.list.d/github-cli.list",
	} {
		if !r.ran(want) {
			t.Errorf("missing %q in %v", want, r.lines)
		}
	}
	list := r.stdin["sudo install -D -m 0644 /dev/stdin /etc/apt/sources.list.d/github-cli.list"]
	if want := "deb [arch=amd64 signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n"; list != want {
		t.Errorf("source list = %q, want %q", list, want)
	}
	if !strings.Contains(stdoutOf(ctx), "gh is missing") {
		t.Errorf("no explanation printed: %q", stdoutOf(ctx))
	}
}

func TestEnsureGH_TooOld_UpgradesOnDemand(t *testing.T) {
	r := &installRunner{tooOld: true}
	_, ctx := installCtx(t, r)

	if err := ensureGH(ctx); err != nil {
		t.Fatalf("ensureGH: %v", err)
	}
	if !r.ran("apt-get install -y gh") || !strings.Contains(stdoutOf(ctx), "too old") {
		t.Errorf("no upgrade: %v / %q", r.lines, stdoutOf(ctx))
	}
}

func TestEnsureGH_Current_InstallsNothing(t *testing.T) {
	r := &installRunner{}
	_, ctx := installCtx(t, r)
	if err := ensureGH(ctx); err != nil {
		t.Fatal(err)
	}
	if r.ran("sudo") || r.ran("apt-get") {
		t.Errorf("a current gh triggered an install: %v", r.lines)
	}
}

func TestEnsureGH_RootNeedsNoSudo(t *testing.T) {
	r := &installRunner{missing: true}
	_, ctx := installCtx(t, r)
	ctx.IsRoot = func() bool { return true }
	// Without sudo the scripted runner does not model apt: only the call
	// shape matters here.
	_ = ensureGH(ctx)
	if r.ran("sudo") {
		t.Errorf("root went through sudo: %v", r.lines)
	}
	if !r.ran("env DEBIAN_FRONTEND=noninteractive apt-get update") {
		t.Errorf("apt did not run directly: %v", r.lines)
	}
}

func TestEnsureGH_SudoPasswordWithoutTTY_ClearError(t *testing.T) {
	r := &installRunner{missing: true, sudoPass: true}
	_, ctx := installCtx(t, r)

	err := ensureGH(ctx)
	if err == nil || !strings.Contains(err.Error(), "run sudo codvps install (installs the current gh)") {
		t.Fatalf("err = %v, want the sudo codvps install hint", err)
	}
	if r.ran("apt-get") {
		t.Errorf("apt ran without root: %v", r.lines)
	}
	// With a terminal sudo may prompt, so the install proceeds.
	r2 := &installRunner{missing: true, sudoPass: true}
	_, ctx2 := installCtx(t, r2)
	ctx2.IsTTY = func() bool { return true }
	if err := ensureGH(ctx2); err != nil {
		t.Fatalf("on a tty: %v", err)
	}
}

// GitHub() installs gh before anything else and then carries on.
func TestGitHub_MissingGH_InstalledBeforeAuth(t *testing.T) {
	r := &installRunner{missing: true}
	env, ctx := installCtx(t, r)
	env.setLoggedIn()

	code, err := GitHub(ctx, false, "skip", false, "Op", "op@example.com", true, false)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v\n%s\n%s", code, err, stdoutOf(ctx), stderrOf(ctx))
	}
	if !r.installed {
		t.Errorf("gh was not installed: %v", r.lines)
	}
}
