package head

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

const (
	cursorUnitPrefix = "cursor-remote@"
	cursorUnitSuffix = ".service"
	cursorUnitGlob   = "cursor-remote@*.service"
)

// CursorOptions are the Cursor head's injected dependencies.
type CursorOptions struct {
	// Layout resolves HOME and the codvps state paths.
	Layout *paths.Layout
	// Git inspects registered checkouts during registry pruning.
	Git runner.Runner
	// Systemd runs every systemctl/journalctl call (systemd --user scope).
	Systemd runner.Runner
	// Diag receives diagnostic output.
	Diag io.Writer
	// Sleep waits between activation polls; tests pass a no-op.
	Sleep func(time.Duration)
}

// Cursor manages the host-wide Cursor head.
type Cursor struct {
	layout *paths.Layout
	git    runner.Runner
	r      runner.Runner
	diag   io.Writer
	sleep  func(time.Duration)
}

// NewCursor builds a Cursor head from opts; every dependency is required.
func NewCursor(opts CursorOptions) (*Cursor, error) {
	switch {
	case opts.Layout == nil:
		return nil, errors.New("cursor head: Layout is required")
	case opts.Git == nil:
		return nil, errors.New("cursor head: Git runner is required")
	case opts.Systemd == nil:
		return nil, errors.New("cursor head: Systemd runner is required")
	case opts.Diag == nil:
		return nil, errors.New("cursor head: Diag writer is required")
	case opts.Sleep == nil:
		return nil, errors.New("cursor head: Sleep is required")
	}
	return &Cursor{layout: opts.Layout, git: opts.Git, r: opts.Systemd, diag: opts.Diag, sleep: opts.Sleep}, nil
}

// errNeedsLogin is the error when enabling a head without a login.
var errCursorNeedsLogin = errors.New("run codvps login cursor first")

// SetEnabled flips the host-wide Cursor head switch and reconciles. Enabling
// requires agent status to succeed first.
func (c *Cursor) SetEnabled(enable bool) error {
	flag := c.layout.CursorHeadFlagPath()
	if enable {
		if err := c.requireCredential(); err != nil {
			return err
		}
		if err := ensureStateDir(c.layout.ConfigDir()); err != nil {
			return err
		}
		if err := fsutil.AtomicWrite(flag, nil, 0o600); err != nil {
			return fmt.Errorf("failed to switch the Cursor head on: %w", err)
		}
		// Print disclosure before reconciling
		_, _ = fmt.Fprintf(c.diag, "Cursor requires a Cursor plan with Cloud Agents. The agent loop and model run in Cursor's cloud; code and context are sent to Cursor. The worker runs with this account's full access; --worker-dir is not a sandbox.\n")
	} else if err := os.Remove(flag); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to switch the Cursor head off: %w", err)
	}
	return c.Reconcile()
}

// Reconcile makes the cursor-remote@ instances match the registry and the
// host-wide switch.
func (c *Cursor) Reconcile() error {
	names, err := repo.PruneVanishedCheckouts(c.layout, c.git, c, false)
	if err != nil {
		return err
	}
	enabled, err := repo.CursorHeadFlagSet(c.layout)
	if err != nil {
		return err
	}
	desired := make(map[string]bool, len(names))
	if enabled {
		for _, name := range names {
			desired[name] = true
		}
	}

	instances, err := c.instances(true)
	if err != nil {
		return err
	}
	var pruneErrs []error
	for _, inst := range instances {
		if desired[inst.name] {
			continue
		}
		if err := c.disableNow(inst.unit); err != nil {
			pruneErrs = append(pruneErrs, err)
		}
	}
	if len(desired) == 0 {
		return errors.Join(pruneErrs...)
	}

	return errors.Join(append(pruneErrs, c.attach(names))...)
}

// attach brings every named head up without requiring credential seeding.
func (c *Cursor) attach(names []string) error {
	if err := c.requireCredential(); err != nil {
		return err
	}

	for _, name := range names {
		unit, err := repo.CursorUnit(name)
		if err != nil {
			return err
		}
		if c.unitHealthy(unit) {
			continue
		}
		c.resetFailed(unit)
		if err := c.systemctl("enable", "--now", unit); err != nil {
			return fmt.Errorf("failed to start the Cursor head for %s: %w", name, err)
		}
		if err := c.waitActive(unit); err != nil {
			if err := c.disableNow(unit); err != nil {
			return fmt.Errorf("failed to disable %s: %w", unit, err)
		}
			return fmt.Errorf("Cursor head for %s failed to stay active: %w", name, err)
		}
	}
	return nil
}

// requireCredential checks that agent is authenticated via status --format json.
func (c *Cursor) requireCredential() error {
	home := c.layout.Home()
	agentBin := filepath.Join(home, ".local", "bin", "agent")
	stdout, _, _, _ := c.r.Run(agentBin, "status", "--format", "json")
	var statusResp struct {
		IsAuthenticated bool `json:"isAuthenticated"`
	}
	if err := json.Unmarshal([]byte(stdout), &statusResp); err != nil {
		return errCursorNeedsLogin
	}
	if !statusResp.IsAuthenticated {
		return errCursorNeedsLogin
	}
	return nil
}

// instances lists every cursor-remote@ instance, optionally only enabled ones.
func (c *Cursor) instances(onlyEnabled bool) ([]cursorInstance, error) {
	var inst []cursorInstance
	out, _, _, _ := c.r.Run("systemctl", "--user", "list-unit-files", cursorUnitGlob, "--no-legend", "--no-pager")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], cursorUnitPrefix) {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(f[0], cursorUnitPrefix), cursorUnitSuffix)
		if !onlyEnabled || strings.HasPrefix(f[1], "enabled") {
			inst = append(inst, cursorInstance{unit: f[0], name: name})
		}
	}
	return inst, nil
}

// unitHealthy reports whether a unit is active and will stay that way.
func (c *Cursor) unitHealthy(unit string) bool {
	out, _, _, _ := c.r.Run("systemctl", "--user", "is-active", unit)
	return strings.TrimSpace(out) == "active"
}

// resetFailed clears the failed state of a unit.
func (c *Cursor) resetFailed(unit string) {
	_, _, _, _ = c.r.Run("systemctl", "--user", "reset-failed", unit)
}

// systemctl runs a systemctl --user command, capturing output.
func (c *Cursor) systemctl(args ...string) error {
	full := append([]string{"systemctl", "--user"}, args...)
	_, stderr, code, err := c.r.Run(full[0], full[1:]...)
	if code != 0 {
		return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr))
	}
	return err
}

// waitActive polls for a unit to become active and stay active.
func (c *Cursor) waitActive(unit string) error {
	for attempt := 0; attempt < activationAttempts; attempt++ {
		c.sleep(activationInterval)
		out, _, _, _ := c.r.Run("systemctl", "--user", "is-active", unit)
		if strings.TrimSpace(out) != "active" {
			continue
		}
		// Unit is active; confirm it stays active for one more interval
		// before saying it is up.
		c.sleep(activationInterval)
		out, _, _, _ = c.r.Run("systemctl", "--user", "is-active", unit)
		if strings.TrimSpace(out) == "active" {
			return nil
		}
	}
	return fmt.Errorf("unit %s did not become active within %d attempts", unit, activationAttempts)
}

// disableNow stops and disables a unit.
func (c *Cursor) disableNow(unit string) error {
	if err := c.systemctl("disable", "--now", unit); err != nil {
		return err
	}
	return nil
}

// DisableCursorUnit is called for repo pruning.
func (c *Cursor) DisableCursorUnit(name string) error {
	unit, err := repo.CursorUnit(name)
	if err != nil {
		return err
	}
	return c.disableNow(unit)
}

// DisableClaudeUnit implements ClaudeUnitDisabler for repo pruning compatibility.
// Cursor doesn't have Claude units, so this is a no-op.
func (c *Cursor) DisableClaudeUnit(name string) error {
	return nil
}

type cursorInstance struct {
	unit string
	name string
}
