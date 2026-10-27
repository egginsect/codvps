// Package session launches native coding CLIs without a service manager.
// Native CLIs retain their own trust, login, sandbox and remote permissions.
package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
	"github.com/egginsect/codvps/internal/shellenv"
)

// Plan is a native executable, arguments, checked working directory and
// child-only environment. It contains no credentials.
type Plan struct {
	Binary    string
	Args      []string
	Dir       string
	Env       []string
	RemoveEnv []string
	// BoundedRetry marks Codex's host-wide start: Execute runs Binary's
	// `remote-control start --json` in-process through head.RunRemoteStart
	// (one bounded retry) instead of replacing codvps with it.
	BoundedRetry bool
}

func provider(name string) (*providers.Provider, error) {
	if name != "claude" && name != "codex" && name != "cursor" {
		return nil, fmt.Errorf("unknown session provider %q (choose claude, codex or cursor)", name)
	}
	return providers.All().Lookup(name), nil
}

// Binary resolves an already-installed CLI. User-space launches never install
// software or change system settings as a side effect.
func Binary(layout *paths.Layout, name string) (string, error) {
	p, err := provider(name)
	if err != nil {
		return "", err
	}
	candidates := []string{filepath.Join(layout.Home(), ".local", "bin", p.Binary)}
	if name == "codex" {
		candidates = append([]string{head.CodexStandalone(filepath.Join(layout.Home(), ".codex"), "codex")}, candidates...)
	}
	for _, path := range candidates {
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0111 != 0 {
			return path, nil
		}
	}
	if path, err := exec.LookPath(p.Binary); err == nil {
		return filepath.Abs(path)
	}
	return "", fmt.Errorf("%s CLI is not installed; install it from the provider's official distribution before retrying", p.Label)
}

func base(layout *paths.Layout, name string) (Plan, error) {
	plan, err := environment(layout, name)
	if err != nil {
		return plan, err
	}
	plan.Binary, err = Binary(layout, name)
	return plan, err
}

// environment is a plan's child environment, before its executable is
// resolved.
func environment(layout *paths.Layout, name string) (Plan, error) {
	var plan Plan
	p, err := provider(name)
	if err != nil {
		return plan, err
	}
	plan.Env, err = settings.Environment(layout)
	if err != nil {
		return plan, err
	}
	if p.Env != nil {
		plan.RemoveEnv = append(plan.RemoveEnv, p.Env.Remove...)
	}
	if name == "codex" {
		plan.Env = append(plan.Env, "CODEX_HOME="+filepath.Join(layout.Home(), ".codex"))
	}
	return plan, nil
}

func checkout(layout *paths.Layout, name string, git runner.Runner) (string, error) {
	path, err := repo.CheckoutPath(layout, name)
	if err != nil {
		return "", err
	}
	if err := repo.IsPrimaryGitCheckout(git, path); err != nil {
		return "", err
	}
	return path, nil
}

// Interactive runs a native CLI in an explicitly registered checkout. Arguments
// are passed as an argv vector, never evaluated by a shell.
func Interactive(layout *paths.Layout, name, repository string, args []string, git runner.Runner) (Plan, error) {
	plan, err := base(layout, name)
	if err != nil {
		return plan, err
	}
	plan.Dir, err = checkout(layout, repository, git)
	if err != nil {
		return plan, err
	}
	plan.Args = append([]string(nil), args...)
	// Interactive use retains native API-key and endpoint choices; the remote
	// launcher alone strips provider variables that conflict with OAuth Remote Control.
	plan.RemoveEnv = nil
	return plan, nil
}

const codexHostWide = "codex Remote is host-wide; omit the repository and select its directory in the connected client"

// Start is the one place that builds how remote control starts, shared by
// the systemd units (through codvps internal remote-start, see UnitStart)
// and manual mode. Claude and Cursor run one worker for a registered
// checkout; Codex is host-wide and starts through the same bounded-retry
// helper as codex-remote@.service (see Plan.BoundedRetry).
func Start(layout *paths.Layout, name, repository string, git runner.Runner) (Plan, error) {
	plan, err := base(layout, name)
	if err != nil {
		return plan, err
	}
	return start(layout, plan, name, repository, git)
}

// UnitStart is Start as the claude-remote@ and cursor-remote@ units run it:
// the same arguments, directory and environment (Cursor keeps the worker
// name and data directory its units have always used), but Binary is the
// executable each unit has always named, resolved later on the operator's
// login-shell PATH: claude by name, Cursor's agent at ~/.local/bin/agent.
func UnitStart(layout *paths.Layout, name, repository string, git runner.Runner) (Plan, error) {
	if name != "claude" && name != "cursor" {
		return Plan{}, fmt.Errorf("unit remote start supports claude and cursor, not %q", name)
	}
	plan, err := environment(layout, name)
	if err != nil {
		return plan, err
	}
	plan.Binary = providers.All().Lookup(name).Binary
	if name == "cursor" {
		plan.Binary = filepath.Join(layout.Home(), ".local", "bin", plan.Binary)
	}
	plan, err = start(layout, plan, name, repository, git)
	if err != nil || name != "cursor" {
		return plan, err
	}
	// Keep the identity units have always registered with Cursor: the
	// hostname-based worker name and its data directory, so upgrading does
	// not turn existing systemd workers into new ones. The worker runs from
	// the checkout (plan.Dir), which is where the unit's shell-exec starts it.
	host, err := hostname()
	if err != nil {
		return plan, fmt.Errorf("cannot name the cursor worker: %w", err)
	}
	plan.Args = []string{"worker", "--name", host + "-" + repository, "--worker-dir", ".", "--data-dir", filepath.Join(layout.Home(), ".local", "share", "cursor-agent", "codvps-workers", repository), "start"}
	return plan, nil
}

// hostname names the Cursor worker a unit starts; tests replace it.
var hostname = os.Hostname

func start(layout *paths.Layout, plan Plan, name, repository string, git runner.Runner) (Plan, error) {
	if name == "codex" {
		if repository != "" {
			return plan, fmt.Errorf("%s", codexHostWide)
		}
		if err := validateStandalone(layout, plan.Binary); err != nil {
			return plan, err
		}
		plan.Args = []string{"remote-control", "start", "--json"}
		plan.Dir = layout.Home()
		plan.BoundedRetry = true
		return plan, nil
	}
	if repository == "" {
		return plan, fmt.Errorf("%s Remote requires a registered repository name", name)
	}
	var err error
	plan.Dir, err = checkout(layout, repository, git)
	if err != nil {
		return plan, err
	}
	switch name {
	case "claude":
		plan.Args = []string{"remote-control", "--spawn", "worktree", "--capacity", "4", "--no-create-session-in-dir", "--remote-control-session-name-prefix", "cloud-" + repository}
	case "cursor":
		plan.Env = append(plan.Env, "NO_OPEN_BROWSER=1")
		plan.Args = []string{"worker", "--name", "codvps-" + repository, "--worker-dir", plan.Dir, "--data-dir", filepath.Join(layout.DataDir(), "cursor-workers", repository), "start"}
	}
	return plan, nil
}

// Stop is Codex's host-wide native stop. Claude and Cursor workers have no
// native stop command: systemd stops their unit, and manual mode signals
// the background process it started.
func Stop(layout *paths.Layout, name, repository string) (Plan, error) {
	if name != "codex" {
		return Plan{}, fmt.Errorf("%s Remote has no native stop command; use codvps remote stop %s [<repo>]", name, name)
	}
	return codexVerb(layout, "stop", repository)
}

func codexVerb(layout *paths.Layout, verb, repository string) (Plan, error) {
	plan, err := base(layout, "codex")
	if err != nil {
		return plan, err
	}
	if repository != "" {
		return plan, fmt.Errorf("%s", codexHostWide)
	}
	plan.Args = []string{"remote-control", verb, "--json"}
	plan.Dir = layout.Home()
	return plan, nil
}

// Remote preserves the vendors' lifecycle models through Start and Stop.
// Claude/Cursor run attached to this terminal for one repository; Codex
// start/stop/pair are host-wide and use its native daemon. No fake systemd
// unit or daemon is created by codvps.
func Remote(layout *paths.Layout, action, name, repository string, git runner.Runner) (Plan, error) {
	if _, err := provider(name); err != nil {
		return Plan{}, err
	}
	if name == "codex" {
		switch action {
		case "start":
			return Start(layout, name, repository, git)
		case "stop":
			return Stop(layout, name, repository)
		case "pair":
			return codexVerb(layout, action, repository)
		}
		return Plan{}, fmt.Errorf("codex Remote supports start, stop and pair; use remote status codex to inspect the local control socket")
	}
	if action != "run" {
		return Plan{}, fmt.Errorf("%s Remote runs one worker per repository: use codvps remote run %s <repo> in the foreground, or codvps remote start|stop %s [<repo>] [--background]", name, name, name)
	}
	return Start(layout, name, repository, git)
}

// Execute replaces codvps so signals and terminal IO belong directly to the
// provider. On launch failure the original working directory is restored.
// A BoundedRetry plan instead runs Codex's start in-process and returns.
func Execute(plan Plan) error {
	if plan.BoundedRetry {
		return boundedRetry(plan, os.Getenv, runner.NewExecRunner(plan.Env...), os.Stdout, os.Stderr)
	}
	return execute(plan, os.Environ(), syscall.Exec)
}

// boundedRetry runs `codex remote-control start --json` from plan.Dir with
// head.RunRemoteStart, the helper codex-remote@.service runs as
// codvps internal codex-remote-start: one stop/start retry after Codex's
// transient errored-relay failure, and nothing else.
func boundedRetry(plan Plan, getenv func(string) string, r runner.Runner, out, diag io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(plan.Dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(cwd) }()
	lookup := func(key string) string {
		value, found := "", false
		for _, entry := range plan.Env {
			if v, ok := strings.CutPrefix(entry, key+"="); ok {
				value, found = v, true
			}
		}
		if found {
			return value
		}
		return getenv(key)
	}
	_, err = head.RunRemoteStart(head.RemoteStartOptions{
		Getenv:   lookup,
		Runner:   r,
		Binary:   filepath.Base(plan.Binary),
		Hostname: os.Hostname,
		Out:      out,
		Diag:     diag,
	})
	return err
}

func execute(plan Plan, environment []string, replace func(string, []string, []string) error) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(plan.Dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := replace(plan.Binary, append([]string{plan.Binary}, plan.Args...), Environ(plan, environment)); err != nil {
		return fmt.Errorf("cannot start %s: %w", plan.Binary, err)
	}
	return nil
}

// Environ is the child environment a plan's process starts with: the given
// environment minus RemoveEnv and every variable Env sets, then Env.
func Environ(plan Plan, environment []string) []string {
	remove := append([]string(nil), plan.RemoveEnv...)
	for _, entry := range plan.Env {
		if key, _, ok := strings.Cut(entry, "="); ok {
			remove = append(remove, key)
		}
	}
	return append(shellenv.Remove(environment, remove...), plan.Env...)
}

// Codex bootstrap copies its package directory. Accept the vendor's current
// symlink within a dedicated standalone tree, but never an alias to a host
// runtime elsewhere. This is not a signature check of the vendor package.
func validateStandalone(layout *paths.Layout, binary string) error {
	const hint = "Codex Remote start requires a dedicated official standalone install under ~/.codex; refusing to bootstrap from an unrelated runtime directory"
	root := filepath.Join(layout.Home(), ".codex", "packages", "standalone")
	if binary != filepath.Join(root, "current", "codex") {
		return fmt.Errorf("%s", hint)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil || physicalRoot != root {
		return fmt.Errorf("%s", hint)
	}
	physicalBinary, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return fmt.Errorf("%s", hint)
	}
	rel, err := filepath.Rel(root, physicalBinary)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s", hint)
	}
	return nil
}
