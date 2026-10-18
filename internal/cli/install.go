package cli

import (
	"errors"
	"fmt"
	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/userinstall"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/install"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/shellenv"
	"github.com/egginsect/codvps/internal/systemd"
)

// installCmd defaults to user CLIs. Host provisioning needs explicit --system.
type installCmd struct{}

func (c *installCmd) Execute(args []string) error {
	system, user := false, false
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--system":
			if system {
				return fmt.Errorf("duplicate --system")
			}
			system = true
		case "--user":
			if user {
				return fmt.Errorf("duplicate --user")
			}
			user = true
		case "--components", "--switch":
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return fmt.Errorf("%s requires a value", arg)
			}
			rest = append(rest, arg, args[i+1])
			i++
		default:
			rest = append(rest, arg)
		}
	}
	if system && user {
		return fmt.Errorf("--system and --user cannot be combined")
	}
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "help") {
		fmt.Println(userinstall.Usage + "\nSystem provisioning requires explicit --system (sudo). " + install.Usage(providers.All()))
		return nil
	}
	if system {
		return install.Install(installOptions(), rest)
	}
	home := os.Getenv("HOME")
	r := runner.NewExecRunner("CODEX_HOME="+filepath.Join(home, ".codex"), "CODEX_INSTALL_DIR="+filepath.Join(home, ".local", "bin"), "CODEX_NON_INTERACTIVE=1", "CODEX_INSTALL_DAEMON_ONLY=0", "CODEX_INSTALL_DEFER_SELECTION=0", "CODEX_INSTALL_IF_LATEST=0", "CODEX_INSTALL_IF_CURRENT=0")
	return userinstall.Install(userinstall.Options{Home: home, EUID: os.Geteuid(), Runner: r, Out: os.Stdout, Diag: os.Stderr}, rest)
}

// uninstallCmd handles `codvps uninstall [--dry-run]` (root, through sudo).
type uninstallCmd struct{}

func (c *uninstallCmd) Execute(args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--dry-run") {
		return invalidUsage(install.UninstallUsage)
	}
	return install.Uninstall(installOptions(), args)
}

// installOptions wires install and uninstall for the real host. The
// operator's state is created by the installed binary running as the
// operator (runuser, with a scrubbed environment so none of root's HOME or
// XDG_* can redirect it).
func installOptions() install.Options {
	r := runner.NewExecRunner()
	ident := repo.NewOSIdentityLookup()
	ns := os.Getenv("CODVPS_NAMESPACE")
	return install.Options{
		Namespace: ns,
		EUID:      os.Geteuid(),
		SudoUser:  os.Getenv("SUDO_USER"),
		Ident:     ident,
		Runner:    r,
		OperatorState: func(op install.Operator, namespace string, providerNames []string) error {
			args := append([]string{"operator-state"}, providerNames...)
			if err := asOperator(r, op, namespace, args...); err != nil {
				return fmt.Errorf("creating the operator state as %s failed: %w", op.Name, err)
			}
			return nil
		},
		AsOperator: func(op install.Operator, namespace string, args ...string) error {
			return asOperator(r, op, namespace, args...)
		},
		ProcRoot:   "/proc",
		Executable: os.Executable,
		Stdin:      os.Stdin,
		IsTTY:      isTerminal(os.Stdin) && isTerminal(os.Stdout),
		Out:        os.Stdout,
		Diag:       os.Stderr,
		Provision:  true,
		Providers:  providers.All(),
	}
}

// asOperator runs the installed `codvps internal <args...>` as the
// operator through runuser, with a scrubbed environment so none of root's
// HOME or XDG_* can redirect it.
func asOperator(r runner.Runner, op install.Operator, namespace string, args ...string) error {
	full := append([]string{"-u", op.Name, "--", "env", "-i",
		"HOME=" + op.Home,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"CODVPS_NAMESPACE=" + namespace,
		systemd.CodvpsBinary, "internal"}, args...)
	_, stderr, code, err := r.Run("runuser", full...)
	if code != 0 || err != nil {
		msg := strings.TrimSpace(stderr)
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return errors.New(strings.TrimPrefix(msg, "codvps: "))
	}
	return nil
}

// isTerminal reports whether f is a real terminal (never /dev/null).
func isTerminal(f *os.File) bool { return fsutil.IsTerminal(f) }

// internalOperatorState is `codvps internal operator-state [<provider>...]`,
// the operator-side half of install: install runs it as the operator
// through runuser, naming the selected providers that keep private
// operator state. HOME names the operator's home; the caller's XDG_*
// never apply.
func internalOperatorState(args []string) error {
	var selected providers.Set
	for _, name := range args {
		p := providers.All().Lookup(name)
		if p == nil || p.Install == nil || p.Install.OperatorState == nil {
			return invalidUsage("codvps internal operator-state [<provider>...]")
		}
		selected = append(selected, p)
	}
	layout, err := paths.NewForHome(os.Getenv("HOME"), os.Getenv("CODVPS_NAMESPACE"))
	if err != nil {
		return err
	}
	return install.EnsureOperatorState(layout, runner.NewExecRunner("GIT_TERMINAL_PROMPT=0"), selected)
}

// doctorComponents are the providers with doctor checks, with their
// selection read from the component registry. An unreadable registry means
// doctor cannot answer.
func doctorComponents(layout *paths.Layout, set providers.Set, heads *head.Heads) ([]doctor.Component, error) {
	reg, err := (components.Catalog{Providers: set}).ReadAt(layout)
	if err != nil {
		return nil, err
	}
	var out []doctor.Component
	for _, p := range set {
		if p.Doctor == nil {
			continue
		}
		out = append(out, doctor.Component{
			Name:     p.Name,
			Selected: reg.Has(p),
			Optional: p.Kind == providers.Switch,
			Suite:    p.Doctor(heads),
		})
	}
	return out, nil
}

// runDoctor is `codvps doctor`: exit 0 healthy, 1 with failures (already
// printed in the report), 2 when doctor could not run its checks.
func runDoctor() error {
	if manual, err := manualDoctor(); manual || err != nil {
		if err != nil {
			code := 2
			if manual {
				code = 1
			}
			return &ExitError{Code: code, Message: err.Error()}
		}
		return nil
	}
	env, err := newHeadEnv()
	if err != nil {
		return &ExitError{Code: 2, Message: err.Error()}
	}
	comps, err := doctorComponents(env.layout, env.providers, env.heads)
	if err != nil {
		return &ExitError{Code: 2, Message: err.Error()}
	}
	report, err := doctor.Run(doctor.Options{
		Layout:     env.layout,
		Runner:     env.runner,
		Operator:   env.operator,
		LookupEnv:  os.LookupEnv,
		LookPath:   exec.LookPath,
		ShellPath:  loginShellPath,
		Out:        os.Stdout,
		Components: comps,
	})
	if err != nil {
		return &ExitError{Code: 2, Message: err.Error()}
	}
	if report.Failures > 0 {
		return &reportedError{err: &ExitError{Code: 1, Message: fmt.Sprintf("doctor found %d failure(s)", report.Failures)}}
	}
	return nil
}

// loginShellPath is the PATH the operator's login shell gives a head whose
// unit starts the launcher with the environment base.
func loginShellPath(base []string) (string, error) {
	env, err := shellenv.Default().WithBase(base).Capture()
	if err != nil {
		return "", err
	}
	return shellenv.PathOf(env), nil
}
