package doctor

import (
	"errors"
	"github.com/egginsect/codvps/internal/head"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/systemd"
)

// CodexUnitRequiredLines are the lines an installed codex-remote@.service
// must carry for the Codex head to run the official daemon lifecycle
// (the reference's installed-unit check, naming codvps's own entry point).
var CodexUnitRequiredLines = []string{
	"Type=oneshot",
	"RemainAfterExit=yes",
	"User=%i",
	"WorkingDirectory=/home/%i",
	"ExecStart=" + systemd.CodvpsBinary + " internal shell-exec --provider codex -- " + systemd.CodvpsBinary + " internal codex-remote-start",
	"ExecStop=/home/%i/.codex/packages/standalone/current/codex remote-control stop --json",
}

// retiredCodexUnitMarkers are directives of the mount-namespace sandbox
// codvps used to put around Codex Remote (now removed). An installed
// unit that still carries one predates the removal and hides the daemon's control
// socket from the host codex CLI.
var retiredCodexUnitMarkers = []string{
	"ExecStartPre=", "ProtectSystem=", "ProtectHome=", "PrivateTmp=", "BindPaths=", "BindReadOnlyPaths=",
}

// CodexProbe is the read-only view of the Codex head doctor needs.
type CodexProbe interface {
	// DaemonStatus reports whether the daemon answers on its control
	// socket and, when it does not, why.
	DaemonStatus() (bool, string, error)
	// EnabledState is `systemctl is-enabled` for the operator's unit.
	EnabledState() (string, error)
	// UnitState is the unit's ActiveState and SubState.
	UnitState() (string, string, error)
}

// CodexSuite is the Codex provider's doctor checks: its login, standalone
// install and unit with the prerequisites, then its daemon. name is the provider's name, binary its
// CLI, and probe its head.
func CodexSuite(name, binary string, probe CodexProbe) Suite {
	st := &codexState{name: name, binary: binary, probe: probe}
	return Suite{
		Tools: []string{binary},
		Prepare: func(*Check) error {
			if probe == nil {
				return errors.New("doctor: the Codex head probe is required")
			}
			// The probe answers are reported, never trusted blindly: "" is
			// shown as not active, which is the conservative reading.
			st.enabled, _ = probe.EnabledState()
			st.active, _, _ = probe.UnitState()
			return nil
		},
		Install: func(d *Check) {
			d.checkCodexInstall(st)
		},
		Health: func(d *Check) {
			d.checkCodexDaemon(st)
		},
	}
}

// codexState is what doctor learns about the Codex head once and reuses.
type codexState struct {
	name    string
	binary  string
	probe   CodexProbe
	enabled string
	active  string
}

func (d *Check) remoteHome() string { return head.CodexRemoteHome(d.opts.Layout) }

// committed reports whether the Codex head is switched on or running,
// which turns a missing prerequisite from a warning into a failure.
func (st *codexState) committed() bool {
	return strings.HasPrefix(st.enabled, "enabled") || st.active == "active"
}

// checkCodexInstall covers the Codex login, the standalone install, the
// installed unit and the private Codex Remote home.
func (d *Check) checkCodexInstall(st *codexState) {
	if legacy := filepath.Join(d.home(), ".codex-remote"); dirPresent(legacy) {
		d.Warn("%s is no longer used: Codex Remote now runs from ~/.codex; move its threads and login there, then set it aside", legacy)
	}
	switch {
	case nonEmptyRegular(filepath.Join(d.remoteHome(), "auth.json")):
		d.Pass("Codex OAuth credential file is present")
	case st.committed():
		d.Fail("Codex OAuth credential file is absent; run codvps login codex")
	default:
		d.Warn("Codex OAuth credential file is absent; run codvps login codex before enabling the Codex head")
	}

	switch {
	case isExecutableFile(head.CodexStandalone(d.remoteHome(), st.binary)):
		d.Pass("Codex standalone install is present")
	case st.committed():
		d.Fail("Codex standalone install is absent; run codvps enable codex to install it")
	default:
		d.Warn("Codex standalone install is absent; codvps enable codex installs it")
	}

	if d.codexUnitIsCurrent() {
		d.Pass("installed Codex unit runs the official daemon lifecycle")
	} else {
		d.Fail("installed Codex unit is missing or is not the current one; run sudo codvps install")
	}
}

func (d *Check) codexUnitIsCurrent() bool {
	path := d.sys(filepath.Join(systemd.SystemUnitDir, systemd.CodexRemoteTemplate))
	data, err := os.ReadFile(path)
	if err != nil || !UnitHasLines(data, CodexUnitRequiredLines) {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		for _, marker := range retiredCodexUnitMarkers {
			if strings.HasPrefix(line, marker) {
				return false
			}
		}
	}
	return isExecutableFile(d.sys(systemd.CodvpsBinary))
}

// UnitHasLines reports whether every want line appears verbatim in unit.
func UnitHasLines(unit []byte, want []string) bool {
	have := map[string]bool{}
	for _, line := range strings.Split(string(unit), "\n") {
		have[line] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// checkCodexDaemon reports the daemon against the head's systemd state, the
// worker binary, and a control-socket link left by the retired sandboxed
// unit.
func (d *Check) checkCodexDaemon(st *codexState) {
	running, reason, _ := st.probe.DaemonStatus()

	d.checkWorkerBinary(st.binary)
	if head.DanglingControlSocket(d.remoteHome()) {
		d.Warn("%s is a dangling symlink left by the retired sandboxed Codex unit, so the codex CLI cannot reach the daemon; run codvps enable codex", head.ControlSocket(d.remoteHome()))
	}

	switch {
	case strings.HasPrefix(st.enabled, "enabled"):
		switch {
		case st.active != "active":
			d.Fail("Codex Remote head is enabled but its systemd unit is %s", orDefault(st.active, "not active"))
		case running:
			d.Pass("Codex Remote head is enabled and its daemon is reachable")
		default:
			d.Fail("Codex Remote systemd unit is active but its daemon is unreachable (%s); inspect: sudo journalctl -u %s", orDefault(reason, "cause unknown"), systemd.CodexUnit(d.opts.Operator))
		}
	case running:
		d.Warn("Codex Remote daemon is running outside the enabled codvps unit")
	case st.active == "active":
		d.Warn("Codex Remote unit is active but its daemon is unreachable (%s)", orDefault(reason, "cause unknown"))
	}

	if running {
		d.checkCodexDaemonShellPath()
	}
}

// checkCodexDaemonShellPath compares the running daemon's PATH with the
// login shell's. The unit is a oneshot whose ExecStart exits after starting
// the daemon, so it has no main process; every top-level process left in
// its cgroup (the daemon and its pid-update loop) is checked.
func (d *Check) checkCodexDaemonShellPath() {
	d.checkHeadShellPath(false, systemd.CodexUnit(d.opts.Operator))
}

// checkWorkerBinary checks that the Codex binary the unit runs -- the
// vendor standalone install -- resolves to an executable.
func (d *Check) checkWorkerBinary(binary string) {
	configured := head.CodexStandalone(d.remoteHome(), binary)
	target, err := filepath.EvalSymlinks(configured)
	if err != nil || !isExecutableFile(target) {
		d.Fail("Codex worker binary is unresolvable; run codvps update codex")
		return
	}
	d.Pass("Codex worker binary is present: %s", target)
}

// privateOperatorPath is the reference's is_private_operator_path: a real
// directory or regular file (never a symlink), owned by the caller, with
// exactly mode.
func privateOperatorPath(path string, dir bool, mode os.FileMode) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if dir != fi.IsDir() || (!dir && !fi.Mode().IsRegular()) {
		return false
	}
	uid, _, ok := fileIDs(fi)
	return ok && uid == os.Geteuid() && fi.Mode().Perm() == mode
}

func nonEmptyRegular(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

func dirPresent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
