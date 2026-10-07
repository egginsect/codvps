package providers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// The Codex CLI: a coding CLI whose one host head turns on Codex Remote
// Control for the operator's ordinary Codex daemon (~/.codex, shared with
// the interactive CLI). codvps adds no sandbox of its own.

// codexName is the Codex provider's name.
const codexName = "codex"

// codexBinary is the Codex CLI's executable.
const codexBinary = "codex"

// codexInstallerURL is the Codex standalone installer.
const codexInstallerURL = "https://chatgpt.com/codex/install.sh"

// legacyCodexUserUnit is the original Codex head: a --user unit that ran
// the daemon against the operator's whole home. install retires it.
const legacyCodexUserUnit = "codex-remote.service"

// codexStandalone is the standalone install's current binary, relative to
// the Codex Remote home.
var codexStandalone = head.CodexStandalone("", codexBinary)

func codex() *Provider {
	return &Provider{
		Name: codexName,
		Kind: CodingCLI,
		Capabilities: Capabilities{
			Install: Yes, Configure: Yes, Auth: Yes, Update: Yes,
			Remove: No, Service: Yes, NativeRemote: Yes,
		},
		Binary: codexBinary,
		Label:  "Codex",
		// Nothing is removed from the operator's shell environment for Codex.
		Env: &Env{},
		// The reference's has_codex_cred: `[[ -s auth.json ]]`.
		Credential: &Credential{Path: filepath.Join(head.CodexRemoteDir, "auth.json"), Stored: NonEmptyFile},
		Login: &Login{
			Summary: "Authenticate Codex",
			Command: login.CLI{
				// The device code has to be read while the CLI waits for it.
				Binary: codexBinary, Args: []string{"login", "--device-auth"},
				// CODEX_HOME is the Codex Remote home, for this child only.
				Env: func(home string) []string { return []string{"CODEX_HOME=" + codexRemoteHome(home)} },
				// The reference's has_codex_cred: the login must have produced a
				// non-empty auth.json in the Codex Remote home.
				After: func(home string) error {
					if auth := filepath.Join(codexRemoteHome(home), "auth.json"); !NonEmptyFile(auth) {
						return fmt.Errorf("codex login completed without creating %s", auth)
					}
					return nil
				},
			},
		},
		Head: &Head{
			Host:        func(h *head.Heads) head.Host { return h.Codex },
			Pair:        func(h *head.Heads) error { return h.Codex.Pair() },
			PairSummary: "Print a short-lived Codex pairing code as JSON",
			Ensure:      func(h *head.Heads) error { return h.Codex.Ensure() },
			Repo: func(h *head.Heads, layout *paths.Layout, r runner.Runner) repo.Head {
				return repo.CodexRepoHead(codexName, repo.NewSystemdHeadReconciler(layout, r, nil, h.Codex, nil))
			},
		},
		Doctor: func(h *head.Heads) doctor.Suite { return doctor.CodexSuite(codexName, codexBinary, h.Codex) },
		Internal: map[string]Internal{
			"codex-remote-start": {
				Usage: "codvps internal codex-remote-start",
				Run:   codexRemoteStart,
			},
		},
		Provision: &Provision{
			Title:      "Codex CLI (standalone installer into ~/" + head.CodexRemoteDir + ")",
			AsOperator: true,
			Present:    []string{"~/" + filepath.Join(head.CodexRemoteDir, codexStandalone)},
			Run:        provisionCodex,
			// The standalone install also updates itself; rerunning the
			// installer brings it to the latest release now.
			Update:  func(p Provisioner, _ string) error { return provisionCodex(p) },
			Restart: Restart{Unit: systemd.CodexUnit},
		},
		Install: &Install{
			Units: func() []systemd.UnitTemplate {
				return append(systemd.CodexSystemUnits(), systemd.CodexWatchdogUserUnits()...)
			},
			StaleDropIns: func(operator string) []systemd.StaleDropIn {
				return []systemd.StaleDropIn{systemd.CodexWorkspacesDropIn(operator)}
			},
			// The codex-remote@ unit runs with PATH=/usr/local/bin:/usr/bin:/bin.
			Links:         []string{codexBinary},
			Detect:        codexDetect,
			OperatorState: codexOperatorState,
			Prepare:       codexPrepare,
			Stop:          codexStop,
			Kept: func(home string) []string {
				return []string{filepath.Join(home, head.CodexRemoteDir) + " and its auth.json (Codex Remote credentials)"}
			},
			UninstallNote: "The " + systemd.CodexRemoteTemplate + " unit file was removed, so the Codex head comes back\n" +
				"DISABLED after a reinstall. Re-enable it with:\n  codvps enable " + codexName + "\n",
		},
	}
}

func codexRemoteHome(home string) string { return filepath.Join(home, head.CodexRemoteDir) }

// codexRemoteStart is codex-remote@.service's ExecStart: start Codex Remote
// Control with one bounded retry, exiting with Codex's own status so a
// persistent failure still fails the unit.
func codexRemoteStart(args []string) error {
	if len(args) != 0 {
		return ErrUsage
	}
	code, err := head.RunRemoteStart(head.RemoteStartOptions{
		Getenv:   os.Getenv,
		Runner:   runner.NewExecRunner(),
		Binary:   codexBinary,
		Hostname: os.Hostname,
		Out:      os.Stdout,
		Diag:     os.Stderr,
	})
	if err != nil {
		return &ExitError{Code: code, Err: err}
	}
	return nil
}

// provisionCodex runs the Codex standalone installer into ~/.codex, Codex's
// own default home.
func provisionCodex(p Provisioner) error {
	home := codexRemoteHome(p.Home())
	if err := p.AsOperator([]string{"CODEX_HOME=" + home, "CODEX_NON_INTERACTIVE=1"},
		"curl -fsSL --proto =https "+codexInstallerURL+" | sh"); err != nil {
		return err
	}
	if bin := filepath.Join(home, codexStandalone); !isExecutable(bin) {
		return fmt.Errorf("the Codex standalone installer did not create %s", bin)
	}
	return nil
}

func codexDetect(h InstallHost) bool {
	return dirExists(codexRemoteHome(h.Operator().Home))
}

// codexOperatorState runs as the operator: it removes the SessionStart hook
// the retired runtime updater registered, the membership manifest the
// retired mount-namespace sandbox read, and a control-socket link the
// sandboxed unit left dangling into its private /tmp (the host's codex CLI
// cannot follow it).
func codexOperatorState(layout *paths.Layout, _ runner.Runner) error {
	home := codexRemoteHome(layout.Home())
	if err := removeCodexSessionHook(home); err != nil {
		return fmt.Errorf("could not remove the retired Codex session hook: %w", err)
	}
	manifest := layout.CodexRepositoriesPath()
	if fi, err := os.Lstat(manifest); err == nil && fi.Mode().IsRegular() {
		if err := os.Remove(manifest); err != nil {
			return fmt.Errorf("could not remove the retired Codex membership manifest %s: %w", manifest, err)
		}
	}
	if _, err := head.RemoveDanglingControlSocket(home); err != nil {
		return err
	}
	return nil
}

// codexPrepare records whether a Codex head should be enabled after
// install (the system unit, or the legacy --user unit it replaces), then
// stops both and retires the legacy one, so an enabled head stays enabled
// across an upgrade.
func codexPrepare(h InstallHost) (Finish, error) {
	unit := systemd.CodexUnit(h.Operator().Name)
	should := h.Succeeds("systemctl", "is-enabled", "--quiet", unit) ||
		h.UserSucceeds("is-enabled", "--quiet", legacyCodexUserUnit)
	if h.Succeeds("systemctl", "is-active", "--quiet", unit) {
		if err := h.Run("systemctl", "stop", unit); err != nil {
			return nil, err
		}
	}
	if h.UserSucceeds("is-active", "--quiet", legacyCodexUserUnit) {
		if err := h.RunUser("stop", legacyCodexUserUnit); err != nil {
			return nil, err
		}
	}
	// Ignored deliberately: on every host that never had the legacy unit
	// there is nothing to disable, and systemctl says so with a failure.
	_ = h.RunUser("disable", legacyCodexUserUnit)
	legacy := h.Sys(filepath.Join(systemd.UserUnitDir, legacyCodexUserUnit))
	if err := os.Remove(legacy); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("failed to remove the legacy unit %s: %w", legacy, err)
	}
	return func(h InstallHost, selected bool) error {
		codexEnableWatchdog(h)
		return codexRestoreHead(h, selected, should)
	}, nil
}

// codexEnableWatchdog brings the watchdog along on a host whose Codex head
// was enabled before the watchdog existed.
func codexEnableWatchdog(h InstallHost) {
	if !h.Succeeds("systemctl", "is-enabled", "--quiet", systemd.CodexUnit(h.Operator().Name)) {
		return
	}
	if err := h.RunUser("enable", "--now", systemd.CodexWatchdogTimer); err != nil {
		h.Warnf("could not enable %s: %v; run codvps enable %s", systemd.CodexWatchdogTimer, err, codexName)
	}
}

// codexRestoreHead re-enables a Codex head that was enabled before
// install: started when a Codex login exists, otherwise left enabled but
// stopped with the next step named.
func codexRestoreHead(h InstallHost, selected, should bool) error {
	unit := systemd.CodexUnit(h.Operator().Name)
	switch {
	case selected && should && NonEmptyFile(filepath.Join(codexRemoteHome(h.Operator().Home), "auth.json")):
		return h.Run("systemctl", "enable", "--now", unit)
	case selected && should:
		if err := h.Run("systemctl", "enable", unit); err != nil {
			return err
		}
		h.Warnf("Codex Remote remains enabled but stopped; run codvps login %[1]s, then codvps enable %[1]s", codexName)
	case should:
		h.Warnf("%s is not a selected component; leaving the previously enabled Codex Remote unit as-is (nothing removed). Reselect it with: sudo codvps install --components claude,%[1]s", codexName)
	}
	return nil
}

// codexStop stops and disables the watchdog timer and the operator's
// Codex head during uninstall.
func codexStop(h UninstallHost) {
	h.Printf("--- %s (user unit) ---\n", systemd.CodexWatchdogTimer)
	stopUserUnit(h, systemd.CodexWatchdogTimer)
	unit := systemd.CodexUnit(h.Operator().Name)
	h.Printf("\n--- %s (system unit) ---\n", unit)
	if h.DryRun() {
		// A failed listing reads as "not present", as in the user-unit case.
		if strings.TrimSpace(h.Output("systemctl", "list-unit-files", unit, "--no-legend", "--no-pager")) != "" {
			h.Printf("DRY-RUN: would stop and disable system unit %s\n", unit)
		} else {
			h.Printf("system unit %s: not present.\n", unit)
		}
		return
	}
	if h.Succeeds("systemctl", "is-active", "--quiet", unit) {
		if err := h.Run("systemctl", "stop", unit); err != nil {
			h.Warnf("failed to stop %s: %v; continuing", unit, err)
		}
	}
	// Ignored deliberately: disabling a unit that was never enabled (or
	// whose template is already gone) fails without anything to undo.
	_ = h.Run("systemctl", "disable", unit)
	h.Printf("stopped and disabled system unit %s\n", unit)
}
