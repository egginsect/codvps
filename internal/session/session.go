// Package session launches native coding CLIs without a service manager.
// Native CLIs retain their own trust, login, sandbox and remote permissions.
package session

import (
	"fmt"
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
	var plan Plan
	p, err := provider(name)
	if err != nil {
		return plan, err
	}
	plan.Binary, err = Binary(layout, name)
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

// Remote preserves the vendors' lifecycle models. Claude/Cursor run attached
// to this terminal for one repository; Codex start/stop/pair are host-wide and
// use its native daemon. No fake systemd unit or daemon is created by codvps.
func Remote(layout *paths.Layout, action, name, repository string, git runner.Runner) (Plan, error) {
	plan, err := base(layout, name)
	if err != nil {
		return plan, err
	}
	if name == "codex" {
		if repository != "" {
			return plan, fmt.Errorf("codex Remote is host-wide; omit the repository and select its directory in the connected client")
		}
		if action == "start" {
			if err := validateStandalone(layout, plan.Binary); err != nil {
				return plan, err
			}
		}
		switch action {
		case "start", "stop", "pair":
			plan.Args = []string{"remote-control", action, "--json"}
		default:
			return plan, fmt.Errorf("codex Remote supports start, stop and pair; use remote status codex to inspect the local control socket")
		}
		plan.Dir = layout.Home()
		return plan, nil
	}
	if action != "run" {
		return plan, fmt.Errorf("%s Remote runs in the foreground: use codvps remote run %s <repo>, and Ctrl+C to stop it", name, name)
	}
	if repository == "" {
		return plan, fmt.Errorf("%s Remote requires a registered repository name", name)
	}
	plan.Dir, err = checkout(layout, repository, git)
	if err != nil {
		return plan, err
	}
	switch name {
	case "claude":
		plan.Args = []string{"remote-control"}
	case "cursor":
		plan.Env = append(plan.Env, "NO_OPEN_BROWSER=1")
		plan.Args = []string{"worker", "--name", "codvps-" + repository, "--worker-dir", plan.Dir, "--data-dir", filepath.Join(layout.DataDir(), "cursor-workers", repository), "start"}
	}
	return plan, nil
}

// Execute replaces codvps so signals and terminal IO belong directly to the
// provider. On launch failure the original working directory is restored.
func Execute(plan Plan) error {
	return execute(plan, os.Environ(), syscall.Exec)
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
	remove := append([]string(nil), plan.RemoveEnv...)
	for _, entry := range plan.Env {
		if key, _, ok := strings.Cut(entry, "="); ok {
			remove = append(remove, key)
		}
	}
	env := append(shellenv.Remove(environment, remove...), plan.Env...)
	if err := replace(plan.Binary, append([]string{plan.Binary}, plan.Args...), env); err != nil {
		return fmt.Errorf("cannot start %s: %w", plan.Binary, err)
	}
	return nil
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
