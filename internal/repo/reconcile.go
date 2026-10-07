package repo

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// HeadReconciler is the Claude, Codex and Cursor head operations in one
// value (ClaudeOps, CodexOps and CursorOps; SystemdHeadReconciler in
// production). The repo heads built from it are what repo.Add/List/Remove
// drive; the full behavior of each head lives in internal/head behind the
// ClaudeHead and CodexHead seams below. repo only needs enough of it to
// attach a newly added repository, prune a vanished one, and detach it on
// remove.
type HeadReconciler interface {
	// ReconcileClaude attaches or detaches the Claude head to match the
	// current registry. Must be safe to call unconditionally from repo add:
	// with the head disabled it only prunes stray instances.
	ReconcileClaude() error

	// ClaudeUnitState reports whether one repository's Claude head unit is
	// enabled and/or active.
	ClaudeUnitState(repoName string) (enabled, active bool, err error)

	// DisableClaudeUnit stops and disables one repository's Claude head
	// unit. Used by repo remove and to prune a vanished checkout.
	DisableClaudeUnit(repoName string) error

	// ClaudeHeadEnabled reports the host-wide Claude head toggle.
	ClaudeHeadEnabled() (bool, error)

	// CodexActive reports whether the Codex Remote daemon is reachable.
	CodexActive() (bool, error)

	// CodexEnabledState returns the raw `systemctl is-enabled` state for
	// the Codex head unit (e.g. "enabled", "disabled", or "" if the unit
	// is not installed).
	CodexEnabledState() (string, error)

	// CursorUnitState reports whether one repository's Cursor head unit is
	// enabled and/or active.
	CursorUnitState(repoName string) (enabled, active bool, err error)

	// DisableCursorUnit stops and disables one repository's Cursor head
	// unit. Used by repo remove and to prune a vanished checkout.
	DisableCursorUnit(repoName string) error

	// CursorHeadEnabled reports the host-wide Cursor head toggle.
	CursorHeadEnabled() (bool, error)

	// ReconcileCursor attaches or detaches the Cursor head to match the
	// current registry. Must be safe to call unconditionally from repo add:
	// with the head disabled it only prunes stray instances.
	ReconcileCursor() error
}

// ClaudeHead is the Claude head's reconciliation entry point
// (internal/head.Claude in production). It is injected rather than imported
// because the head package itself builds on this package's registry
// pruning.
type ClaudeHead interface {
	// Reconcile attaches every registered repository while the head is
	// enabled and detaches every other claude-remote@ instance.
	Reconcile() error
}

// CursorHead is the Cursor head's reconciliation entry point
// (internal/head.Cursor in production). It is injected rather than imported
// because the head package itself builds on this package's registry
// pruning.
type CursorHead interface {
	// Reconcile attaches every registered repository while the head is
	// enabled and detaches every other cursor-remote@ instance.
	Reconcile() error
}

// CodexHead is the Codex head as repo list reads it (internal/head.Codex
// in production), injected for the same reason as ClaudeHead.
type CodexHead interface {
	// DaemonRunning probes the Codex Remote daemon's control socket.
	DaemonRunning() (bool, error)
	// EnabledState is HeadReconciler.CodexEnabledState.
	EnabledState() (string, error)
}

// ClaudeUnitDisabler stops and disables one repository's Claude head unit.
// It is the only head operation registry pruning needs.
type ClaudeUnitDisabler interface {
	DisableClaudeUnit(repoName string) error
}

// SystemdHeadReconciler is the production HeadReconciler: it answers the
// per-repository Claude unit queries from `systemctl --user` itself and
// delegates each head's lifecycle to the injected head.
type SystemdHeadReconciler struct {
	layout      *paths.Layout
	r           runner.Runner
	userSystemd *systemd.Manager
	claude      ClaudeHead
	codex       CodexHead
	cursor      CursorHead
}

// NewSystemdHeadReconciler creates a SystemdHeadReconciler. r runs the
// `systemctl --user` calls for per-repository Claude/Cursor units (a real
// runner.Runner in production, a fake in tests); claude, codex, and cursor
// perform each head's reconciliation.
func NewSystemdHeadReconciler(layout *paths.Layout, r runner.Runner, claude ClaudeHead, codex CodexHead, cursor CursorHead) *SystemdHeadReconciler {
	return &SystemdHeadReconciler{
		layout:      layout,
		r:           r,
		userSystemd: systemd.NewUserManager(r),
		claude:      claude,
		codex:       codex,
		cursor:      cursor,
	}
}

// ClaudeUnit builds a claude-remote@<name>.service unit string. name is
// re-validated here: this is the security boundary between a repository
// name and a systemd instance string, so it must hold even if a caller
// forwards an unsafe name from a corrupted registry.
func ClaudeUnit(name string) (string, error) {
	if err := registry.ValidateName(name); err != nil {
		return "", fmt.Errorf("refusing to build a systemd unit name from an unsafe repository name: %w", err)
	}
	return fmt.Sprintf("claude-remote@%s.service", name), nil
}

// ClaudeUnitState reports the given repository's Claude head unit state.
// Query failures are treated the same way the reference treats them (best
// effort, `|| true`): a query that cannot produce a definite answer reports
// "not enabled"/"not active" rather than failing the caller.
func (h *SystemdHeadReconciler) ClaudeUnitState(name string) (bool, bool, error) {
	unit, err := ClaudeUnit(name)
	if err != nil {
		return false, false, err
	}
	out, _, _, _ := h.r.Run("systemctl", "--user", "is-enabled", unit)
	enabled := strings.HasPrefix(strings.TrimSpace(out), "enabled")
	active := h.userSystemd.IsActive(unit)
	return enabled, active, nil
}

// DisableClaudeUnit stops and disables one repository's Claude head unit.
func (h *SystemdHeadReconciler) DisableClaudeUnit(name string) error {
	return DisableAndStopClaudeUnit(h.r, name)
}

// DisableAndStopClaudeUnit disables and then stops claude-remote@<name>
// through the systemd --user manager reached via r. Both failures
// propagate: a head that cannot be detached must not be reported as gone.
func DisableAndStopClaudeUnit(r runner.Runner, name string) error {
	unit, err := ClaudeUnit(name)
	if err != nil {
		return err
	}
	user := systemd.NewUserManager(r)
	if err := user.Disable(unit); err != nil {
		return err
	}
	return user.Stop(unit)
}

// ClaudeHeadEnabled reports the host-wide Claude head toggle: presence of
// the claude-head-enabled flag file under the codvps config directory.
func (h *SystemdHeadReconciler) ClaudeHeadEnabled() (bool, error) {
	return ClaudeHeadFlagSet(h.layout)
}

// ClaudeHeadFlagSet reports whether the host-wide Claude head flag file
// exists. Any stat failure other than "does not exist" is an error.
func ClaudeHeadFlagSet(layout *paths.Layout) (bool, error) {
	flagPath := layout.ClaudeHeadFlagPath()
	_, err := os.Stat(flagPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check Claude head flag: %w", err)
}

// CursorUnit builds a cursor-remote@<name>.service unit string. name is
// re-validated here: this is the security boundary between a repository
// name and a systemd instance string, so it must hold even if a caller
// forwards an unsafe name from a corrupted registry.
func CursorUnit(name string) (string, error) {
	if err := registry.ValidateName(name); err != nil {
		return "", fmt.Errorf("refusing to build a systemd unit name from an unsafe repository name: %w", err)
	}
	return fmt.Sprintf("cursor-remote@%s.service", name), nil
}

// CursorUnitState reports the given repository's Cursor head unit state.
// Query failures are treated the same way as Claude (best effort).
func (h *SystemdHeadReconciler) CursorUnitState(name string) (bool, bool, error) {
	unit, err := CursorUnit(name)
	if err != nil {
		return false, false, err
	}
	out, _, _, _ := h.r.Run("systemctl", "--user", "is-enabled", unit)
	enabled := strings.HasPrefix(strings.TrimSpace(out), "enabled")
	active := h.userSystemd.IsActive(unit)
	return enabled, active, nil
}

// DisableCursorUnit stops and disables one repository's Cursor head unit.
func (h *SystemdHeadReconciler) DisableCursorUnit(name string) error {
	return DisableAndStopCursorUnit(h.r, name)
}

// DisableAndStopCursorUnit disables and then stops cursor-remote@<name>
// through the systemd --user manager reached via r.
func DisableAndStopCursorUnit(r runner.Runner, name string) error {
	unit, err := CursorUnit(name)
	if err != nil {
		return err
	}
	user := systemd.NewUserManager(r)
	if err := user.Disable(unit); err != nil {
		return err
	}
	return user.Stop(unit)
}

// CursorHeadEnabled reports the host-wide Cursor head toggle.
func (h *SystemdHeadReconciler) CursorHeadEnabled() (bool, error) {
	return CursorHeadFlagSet(h.layout)
}

// CursorHeadFlagSet reports whether the host-wide Cursor head flag file
// exists. Any stat failure other than "does not exist" is an error.
func CursorHeadFlagSet(layout *paths.Layout) (bool, error) {
	flagPath := layout.CursorHeadFlagPath()
	_, err := os.Stat(flagPath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("failed to check Cursor head flag: %w", err)
}

// CodexActive probes the Codex daemon through the injected Codex head.
func (h *SystemdHeadReconciler) CodexActive() (bool, error) {
	if h.codex == nil {
		return false, errNoCodexHead
	}
	return h.codex.DaemonRunning()
}

var (
	errNoClaudeHead = errors.New("internal error: SystemdHeadReconciler was built without a Claude head")
	errNoCodexHead  = errors.New("internal error: SystemdHeadReconciler was built without a Codex head")
	errNoCursorHead = errors.New("internal error: SystemdHeadReconciler was built without a Cursor head")
)

// ShowUnitProperties runs `systemctl [--user] show <unit> -p <prop>...` and
// parses its key=value lines. A failed query returns an empty map: every
// caller is a read-only probe for which "no answer" is itself the state to
// report (the reference suffixes each such probe with `|| true`), and the
// state-changing calls that follow a probe surface real manager failures.
func ShowUnitProperties(r runner.Runner, user bool, unit string, props ...string) map[string]string {
	var args []string
	if user {
		args = append(args, "--user")
	}
	args = append(args, "show", unit)
	for _, p := range props {
		args = append(args, "-p", p)
	}
	values := make(map[string]string, len(props))
	out, _, code, err := r.Run("systemctl", args...)
	if err != nil || code != 0 {
		return values
	}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	return values
}

// CodexEnabledState delegates to the injected Codex head.
func (h *SystemdHeadReconciler) CodexEnabledState() (string, error) {
	if h.codex == nil {
		return "", errNoCodexHead
	}
	return h.codex.EnabledState()
}

// ReconcileClaude delegates to the injected Claude head.
func (h *SystemdHeadReconciler) ReconcileClaude() error {
	if h.claude == nil {
		return errNoClaudeHead
	}
	return h.claude.Reconcile()
}

// ReconcileCursor delegates to the injected Cursor head.
func (h *SystemdHeadReconciler) ReconcileCursor() error {
	if h.cursor == nil {
		return errNoCursorHead
	}
	return h.cursor.Reconcile()
}
