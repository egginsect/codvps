package providers

import (
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// The install and uninstall facets. `codvps install` is the single manager
// of everything declared here: it writes (and overwrites) every unit file
// and link a provider declares, and `codvps uninstall` removes exactly
// that declared set. There is no separate record of what was installed;
// the definitions are the record.

// Operator is the login account install and uninstall act for.
type Operator struct {
	Name string
	Home string
	UID  int
	GID  int
}

// InstallHost is what a provider's install hooks may do: every action goes
// through install's own injected runner and system root, so a hook is
// unit-tested with the same fakes as install itself.
type InstallHost interface {
	// Operator is the account codvps is installed for.
	Operator() Operator
	// Layout resolves the operator's codvps paths under the system root.
	Layout() *paths.Layout
	// Sys maps a production system path under the system root.
	Sys(path string) string
	// Run runs a root command and fails with its stderr.
	Run(name string, args ...string) error
	// Succeeds reports whether a root command exits 0.
	Succeeds(name string, args ...string) bool
	// RunUser runs `systemctl --user <args>` in the operator's manager.
	RunUser(args ...string) error
	// UserSucceeds reports whether `systemctl --user <args>` exits 0.
	UserSucceeds(args ...string) bool
	// Output is a read-only root command's stdout ("" on failure).
	Output(name string, args ...string) string
	// UserOutput is a read-only listing from the operator's manager ("" on
	// failure).
	UserOutput(args ...string) string
	// VerifyOwned requires path to be a real directory (dir) or regular
	// file owned by the operator with exactly mode.
	VerifyOwned(path string, dir bool, mode os.FileMode) error
	// Printf reports progress; Warnf reports a warning.
	Printf(format string, args ...any)
	Warnf(format string, args ...any)
}

// UninstallHost is an InstallHost that may be a dry run.
type UninstallHost interface {
	InstallHost
	// DryRun is true for `codvps uninstall --dry-run`: hooks describe what
	// they would do and change nothing.
	DryRun() bool
}

// Install is a provider's part of `codvps install` and `codvps uninstall`.
type Install struct {
	// Units are the unit files codvps installs for the provider. They are
	// written whether or not the provider is selected (a unit file that is
	// never enabled is inert), so selecting it later needs no new files.
	Units func() []systemd.UnitTemplate
	// StaleDropIns are drop-in files an earlier codvps generated for the
	// provider's units and no longer does. Install removes each one whose
	// contents codvps recognises as its own, and its directory when nothing
	// else is in it.
	StaleDropIns func(operator string) []systemd.StaleDropIn
	// Links are executables in the operator's ~/.local/bin that the
	// systemd units need on their PATH: install links /usr/local/bin/<name>
	// to each while the provider is selected, and a selected provider whose
	// link cannot be made fails the install.
	Links []string
	// Preflight refuses a host shape that would make a privileged step
	// unsafe. It runs for every provider before anything is changed.
	Preflight func(h InstallHost) error
	// Detect reports whether an earlier install left this provider on a
	// host that has no component registry yet.
	Detect func(h InstallHost) bool
	// OperatorState creates the provider's private operator state. It runs
	// as the operator (`codvps internal operator-state <provider>...`), never
	// as root, and only for a selected provider.
	OperatorState func(layout *paths.Layout, git runner.Runner) error
	// VerifyOperatorState checks, as root, what OperatorState created
	// before any service can use it.
	VerifyOperatorState func(h InstallHost) error
	// Prepare runs before the unit files are rewritten (for every
	// provider) and returns what Finish must know.
	Prepare func(h InstallHost) (Finish, error)
	// Configure runs for a selected provider once both managers have
	// reloaded the new unit files.
	Configure func(h InstallHost) error
	// Stop stops and disables the provider's units during uninstall.
	Stop func(h UninstallHost)
	// Kept names what uninstall leaves in place for this provider
	// (credentials, state), one line each, for the closing report.
	Kept func(home string) []string
	// UninstallNote is printed at the end of uninstall.
	UninstallNote string
}

// Finish completes a provider's install after the links are in place;
// selected reports whether the provider is part of this install.
type Finish func(h InstallHost, selected bool) error

// Provision is how a provider's CLI gets onto the host and stays current:
// the vendor's own installer and updater, never a codvps-managed copy.
// `codvps enable` and `codvps login` install a missing CLI on demand;
// `codvps update` runs Update.
type Provision struct {
	// Title names the install step.
	Title string
	// AsOperator steps run as the operator (a vendor installer that writes
	// into the operator's home); the others run as root.
	AsOperator bool
	// Present are the paths whose executable presence means the CLI is
	// installed: "~/"-prefixed paths are under the operator's home, the
	// rest are system paths. The first present one is the CLI Update and
	// the version probe run.
	Present []string
	// Run installs the CLI.
	Run func(p Provisioner) error
	// Update brings the installed CLI (bin, an absolute path) to the
	// vendor's latest release.
	Update func(p Provisioner, bin string) error
	// Restart names the head units that run the CLI; an update that
	// changed its version restarts the active ones.
	Restart Restart
	// Note, for a provider with nothing to provision, is shown in the plan
	// instead of a step while it is selected.
	Note string
}

// Restart names a provider's head units: every unit matching Glob, or the
// operator's Unit. User units are the operator's --user units. A provider
// whose vendor updater restarts its own service names neither.
type Restart struct {
	User bool
	Glob string
	Unit func(operator string) string
}

// Provisioner is what a provisioning step may do: every command is shown,
// then run through install's runner with its output streamed.
type Provisioner interface {
	// Home is the operator's home.
	Home() string
	// Cmd runs a root command.
	Cmd(name string, args ...string) error
	// AsOperator runs a shell snippet as the operator with a scrubbed
	// environment plus env.
	AsOperator(env []string, script string) error
}

// stopUserUnit stops and disables one of the operator's --user units
// during uninstall.
func stopUserUnit(h UninstallHost, unit string) {
	if h.DryRun() {
		if strings.TrimSpace(h.UserOutput("list-unit-files", unit, "--no-legend", "--no-pager")) != "" {
			h.Printf("DRY-RUN: would stop and disable user unit %s\n", unit)
		} else {
			h.Printf("user unit %s: not present.\n", unit)
		}
		return
	}
	// Ignored deliberately: a timer that was never enabled or installed has
	// nothing to disable, and systemctl reports that as a failure.
	_ = h.RunUser("disable", "--now", unit)
	h.Printf("stopped and disabled user unit %s\n", unit)
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
