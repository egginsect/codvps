// Package login implements "codvps login <provider>|github".
//
// A coding CLI's login is a thin delegation to that CLI's own login
// command through internal/runner (Run, driven by the provider's
// definition). login github reproduces the reference guided
// flow: a read-only preflight, gh authentication, SSH key selection or
// generation, key registration, known_hosts pinning, an optional SSH config
// block, git identity, and a final SSH verification, each reported in a
// summary table with an ok/pending/skipped/failed status.
package login

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/runner"
)

// Status is the per-step outcome reported in the final summary table.
type Status string

// The four states a step can end in. Every failure path aborts the whole
// flow before the summary prints, so StatusFailed is never actually shown by
// the summary today; it exists so a step function has a way to report a
// hard error uniformly and so the table format stays complete.
const (
	StatusOK      Status = "ok"
	StatusPending Status = "pending"
	StatusSkipped Status = "skipped"
	StatusFailed  Status = "failed"
)

// StepResult records one step's outcome for the summary table.
type StepResult struct {
	Name   string
	Status Status
	Detail string
}

// Context carries the dependencies every login step needs. Tests construct
// it directly with a temp HOME and a Runner backed by fake executables on a
// temp PATH; production code uses NewContext.
type Context struct {
	Runner runner.Runner

	// HomeDir is the operator's home directory. Never the real process HOME
	// in tests.
	HomeDir string

	// EnvRunner builds the Runner for a login command that needs extra
	// environment (CLI.Env); nil means a real subprocess runner.
	EnvRunner func(env []string) runner.Runner

	// Stdin/Stdout/Stderr are used for interactive prompts and messages.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// IsTTY reports whether Stdin is attached to an interactive terminal.
	// Production code defaults this to a real stdin check; tests override it
	// to exercise interactive prompt code paths deterministically.
	IsTTY func() bool

	// IsRoot reports whether the process already runs as root, so installing
	// gh on demand needs no sudo. Nil means not root.
	IsRoot func() bool

	// SysRoot prefixes the system paths read when deciding whether gh's apt
	// repository is already set up ("" in production, a temp dir in tests).
	SysRoot string

	// AssumeYes (--yes) answers every prompt with its default, even on a
	// terminal. Without a terminal prompts always take their defaults.
	AssumeYes bool
}

// NewContext creates a login Context wired to the real process: the real
// exec.Command-backed Runner, the real home directory, and real std streams.
func NewContext() *Context {
	homeDir, _ := os.UserHomeDir()
	return &Context{
		Runner:  runner.NewExecRunner(),
		HomeDir: homeDir,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		IsTTY:   defaultIsTTY,
		IsRoot:  func() bool { return os.Geteuid() == 0 },
	}
}

// defaultIsTTY reports whether the real process stdin is a terminal, not
// merely a character device such as /dev/null: without one, prompts take
// their default answers instead of reading a stdin nobody can type into.
func defaultIsTTY() bool {
	return fsutil.IsTerminal(os.Stdin)
}

// out returns ctx.Stdout, defaulting to os.Stdout when unset.
func (ctx *Context) out() io.Writer {
	if ctx.Stdout != nil {
		return ctx.Stdout
	}
	return os.Stdout
}

// errOut returns ctx.Stderr, defaulting to os.Stderr when unset.
func (ctx *Context) errOut() io.Writer {
	if ctx.Stderr != nil {
		return ctx.Stderr
	}
	return os.Stderr
}

// printf writes a formatted line to ctx.out(), swallowing the (unlikely)
// write error deliberately: output-formatting failures on stdout are not
// actionable and every other Runner/OS error in this package is already
// checked and surfaced.
func (ctx *Context) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(ctx.out(), format, args...)
}

// errPrintf writes a formatted line to ctx.errOut(), for the same reason
// printf discards the write error.
func (ctx *Context) errPrintf(format string, args ...any) {
	_, _ = fmt.Fprintf(ctx.errOut(), format, args...)
}

func (ctx *Context) sshDir() string {
	return filepath.Join(ctx.HomeDir, ".ssh")
}

// keyPath is the fixed, codvps-managed GitHub SSH key location.
func (ctx *Context) keyPath() string {
	return filepath.Join(ctx.sshDir(), "codvps_github_ed25519")
}

// CLI is a coding CLI's own login command (its definition lives in
// internal/providers): codvps runs it attached to the operator's terminal,
// because the CLI prompts for a code or shows a device code while it waits.
type CLI struct {
	// Binary and Args are the login command (for example `claude auth
	// login`).
	Binary string
	Args   []string
	// Hint is printed before the terminal is handed to the CLI.
	Hint string
	// Env is added to the login command's own environment only, never
	// exported to codvps itself or any other command.
	Env func(home string) []string
	// ExecutablePaths are home-relative paths where Binary may be located;
	// checked before PATH to resolve the absolute path.
	ExecutablePaths []string
	// After runs once the command succeeded: it seeds what the head needs,
	// or confirms the login produced a stored credential. Its error is the
	// command's result, as is.
	After func(home string) error
}

// Run runs cli's login command for the operator. Component gating is the
// caller's job, before this runs.
func Run(ctx *Context, cli CLI) error {
	if cli.Hint != "" {
		ctx.printf("%s", cli.Hint)
	}

	// Resolve binary path using ExecutablePaths if available
	binary := cli.Binary
	if len(cli.ExecutablePaths) > 0 {
		if resolved := resolveBinary(ctx.HomeDir, binary, cli.ExecutablePaths); resolved != "" {
			binary = resolved
		}
	}

	r := ctx.Runner
	if cli.Env != nil {
		if env := cli.Env(ctx.HomeDir); len(env) > 0 {
			r = ctx.envRunner(env)
		}
	}
	exitCode, err := r.RunWithIO(binary, cli.Args, ctx.Stdin, ctx.out(), ctx.errOut())
	if exitCode != 0 || err != nil {
		return runFailure(strings.Join(append([]string{cli.Binary}, cli.Args...), " "), exitCode, err)
	}
	if cli.After != nil {
		return cli.After(ctx.HomeDir)
	}
	return nil
}

// envRunner is a Runner whose children get env on top of codvps's own
// environment.
func (ctx *Context) envRunner(env []string) runner.Runner {
	if ctx.EnvRunner != nil {
		return ctx.EnvRunner(env)
	}
	return runner.NewExecRunner(env...)
}

// resolveBinary tries to locate binary at home-relative paths. Returns the
// absolute path if found (following symlinks), or empty string otherwise.
func resolveBinary(home, binary string, paths []string) string {
	for _, relPath := range paths {
		// Expand ~ to home if necessary
		path := relPath
		if strings.HasPrefix(path, "~/") {
			path = filepath.Join(home, path[2:])
		} else if strings.HasPrefix(path, "~") {
			path = filepath.Join(home, path[1:])
		} else if !strings.HasPrefix(path, "/") {
			// Relative paths are relative to home
			path = filepath.Join(home, path)
		}

		// Check if executable exists (follow symlinks with os.Stat)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return path
		}
	}
	return ""
}

// runFailure builds a uniform error for a failed subprocess call, wrapping
// the underlying error when there is one instead of formatting a nil error.
func runFailure(what string, exitCode int, err error) error {
	if err != nil {
		return fmt.Errorf("%s failed: exit %d: %w", what, exitCode, err)
	}
	return fmt.Errorf("%s failed: exit %d", what, exitCode)
}
