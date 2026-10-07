// Package shellenv gives a long-running head the operator's login-shell
// environment instead of the fixed environment its systemd unit carries.
//
// The environment is captured fresh on every start (nothing is cached): the
// operator's login interactive shell runs as a child with no stdin and a
// timeout, prints a unique marker, then dumps its environment with
// `env -0`. Only what follows the marker is parsed, so anything the rc
// files print is ignored. The variables a provider must not inherit are
// removed AFTER the shell ran, so an rc file cannot export them back. The
// process is then replaced with the command (syscall.Exec), never run as a
// child.
//
// When the shell cannot be run, timed out, or printed no marker, the
// command still starts, with the current (unit) environment minus the same
// removals, after one line on Diag saying so.
package shellenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultTimeout bounds the login shell's run.
const DefaultTimeout = 10 * time.Second

// DefaultPasswdPath is where the account's shell is read when $SHELL is
// unset.
const DefaultPasswdPath = "/etc/passwd"

// fallbackShell is used when neither $SHELL nor the passwd entry names one.
const fallbackShell = "/bin/sh"

// Options are the injectable dependencies.
type Options struct {
	// Environ is the current process environment ("KEY=VALUE" entries).
	Environ func() []string
	// PasswdPath is the passwd file read for the account's shell when
	// $SHELL is unset.
	PasswdPath string
	// UID is the account whose passwd entry is looked up.
	UID int
	// Timeout bounds the login shell.
	Timeout time.Duration
	// Diag receives the one-line note printed when the shell environment
	// is unavailable.
	Diag io.Writer
	// Exec replaces the process (syscall.Exec in production): the resolved
	// executable path, the full argv (argv[0] first) and the environment.
	Exec func(path string, argv []string, env []string) error
}

// Default is the production wiring.
func Default() Options {
	return Options{
		Environ:    os.Environ,
		PasswdPath: DefaultPasswdPath,
		UID:        os.Getuid(),
		Timeout:    DefaultTimeout,
		Diag:       os.Stderr,
		Exec:       syscall.Exec,
	}
}

// Shell is the login shell to run: $SHELL, else the account's passwd
// entry, else /bin/sh.
func (o Options) Shell() string {
	if sh := envValue(o.Environ(), "SHELL"); sh != "" {
		return sh
	}
	if sh := passwdShell(o.PasswdPath, o.UID); sh != "" {
		return sh
	}
	return fallbackShell
}

// passwdShell is the shell field of the passwd entry for uid, or "".
func passwdShell(path string, uid int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), ":")
		if len(fields) < 7 {
			continue
		}
		if n, err := strconv.Atoi(fields[2]); err == nil && n == uid {
			return strings.TrimSpace(fields[6])
		}
	}
	return ""
}

// WithBase is o with the environment the login shell starts from replaced
// by base plus SHELL, the shell o would run. It is how a check reproduces
// the launcher's starting environment (the unit's) instead of the
// caller's.
func (o Options) WithBase(base []string) Options {
	shell := o.Shell()
	env := append(append([]string(nil), Remove(base, "SHELL")...), "SHELL="+shell)
	o.Environ = func() []string { return env }
	return o
}

// Capture runs the login shell and returns its environment. The error says
// why it is unavailable.
func (o Options) Capture() ([]string, error) {
	marker, err := newMarker()
	if err != nil {
		return nil, err
	}
	shell := o.Shell()
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	script := `printf '%s\0' ` + marker + `; exec env -0`
	cmd := exec.CommandContext(ctx, shell, "-lic", script)
	cmd.Env = o.Environ()
	cmd.Stdin = nil // /dev/null
	var out bytes.Buffer
	cmd.Stdout = &out
	// A new session: no controlling terminal for an interactive shell to
	// fight over, and the whole group can be killed on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second

	err = cmd.Run()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%s timed out after %s", shell, timeout)
	case err != nil:
		return nil, fmt.Errorf("%s failed: %v", shell, err)
	}
	env, ok := afterMarker(out.Bytes(), marker)
	if !ok {
		return nil, fmt.Errorf("%s printed no environment marker", shell)
	}
	return env, nil
}

func newMarker() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cannot make a marker: %w", err)
	}
	return "CODVPS-ENV-" + hex.EncodeToString(b), nil
}

// afterMarker parses the NUL-separated environment that follows the marker.
func afterMarker(out []byte, marker string) ([]string, bool) {
	i := bytes.Index(out, []byte(marker+"\x00"))
	if i < 0 {
		return nil, false
	}
	var env []string
	for _, entry := range bytes.Split(out[i+len(marker)+1:], []byte{0}) {
		if bytes.Contains(entry, []byte("=")) {
			env = append(env, string(entry))
		}
	}
	return env, len(env) > 0
}

// Remove returns env without the named variables.
func Remove(env []string, names ...string) []string {
	drop := make(map[string]bool, len(names))
	for _, n := range names {
		drop[n] = true
	}
	out := make([]string, 0, len(env))
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && drop[k] {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Environment is the environment a head starts with: the login shell's
// minus remove, or, when the shell is unavailable, the current environment
// minus remove after one line on Diag.
func (o Options) Environment(remove []string) []string {
	env, err := o.Capture()
	if err != nil {
		if o.Diag != nil {
			_, _ = fmt.Fprintf(o.Diag, "codvps shell-exec: shell environment unavailable (%v); starting with the unit environment\n", err)
		}
		env = o.Environ()
	}
	return Remove(env, remove...)
}

// Run replaces the process with argv, started with Environment(remove).
// argv[0] is resolved with the new environment's PATH. It returns only
// when the command could not be started.
func (o Options) Run(remove []string, argv []string) error {
	if len(argv) == 0 {
		return errors.New("shell-exec: no command given")
	}
	env := o.Environment(remove)
	path, err := lookPath(argv[0], envValue(env, "PATH"))
	if err != nil {
		return err
	}
	if err := o.Exec(path, argv, env); err != nil {
		return fmt.Errorf("cannot execute %s: %w", path, err)
	}
	return nil
}

// PathOf is the PATH in env.
func PathOf(env []string) string { return envValue(env, "PATH") }

func envValue(env []string, name string) string {
	val := ""
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			val = v
		}
	}
	return val
}

// lookPath resolves cmd against pathList: a name with a slash is used as
// is.
func lookPath(cmd, pathList string) (string, error) {
	if strings.Contains(cmd, "/") {
		return cmd, nil
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, cmd)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH=%s", cmd, pathList)
}
