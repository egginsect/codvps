// Package systemd provides systemd unit operations.
package systemd

import (
	"fmt"
	"strings"

	"github.com/egginsect/codvps/internal/runner"
)

// Manager handles systemd unit operations.
type Manager struct {
	r    runner.Runner
	user bool // true for systemctl --user, false for system
}

// NewUserManager creates a manager for user-scoped units.
func NewUserManager(r runner.Runner) *Manager {
	return &Manager{r: r, user: true}
}

// NewSystemManager creates a manager for system-scoped units.
func NewSystemManager(r runner.Runner) *Manager {
	return &Manager{r: r, user: false}
}

// systemctlArgs returns base arguments for systemctl call.
func (m *Manager) systemctlArgs() []string {
	if m.user {
		return []string{"systemctl", "--user"}
	}
	return []string{"systemctl"}
}

// Enable enables a unit.
func (m *Manager) Enable(unit string) error {
	args := append(m.systemctlArgs(), "enable", unit)
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		if strings.Contains(stderr, "is not a unit file") {
			return fmt.Errorf("unit %q not found", unit)
		}
		return fmt.Errorf("enable %q failed: %s", unit, strings.TrimSpace(stderr))
	}
	return err
}

// Disable disables a unit.
func (m *Manager) Disable(unit string) error {
	args := append(m.systemctlArgs(), "disable", unit)
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		if strings.Contains(stderr, "is not a unit file") {
			return fmt.Errorf("unit %q not found", unit)
		}
		return fmt.Errorf("disable %q failed: %s", unit, strings.TrimSpace(stderr))
	}
	return err
}

// Start starts a unit.
func (m *Manager) Start(unit string) error {
	args := append(m.systemctlArgs(), "start", unit)
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		if strings.Contains(stderr, "not found") {
			return fmt.Errorf("unit %q not found", unit)
		}
		return fmt.Errorf("start %q failed: %s", unit, strings.TrimSpace(stderr))
	}
	return err
}

// Stop stops a unit.
func (m *Manager) Stop(unit string) error {
	args := append(m.systemctlArgs(), "stop", unit)
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		if strings.Contains(stderr, "not found") {
			return fmt.Errorf("unit %q not found", unit)
		}
		return fmt.Errorf("stop %q failed: %s", unit, strings.TrimSpace(stderr))
	}
	return err
}

// Restart restarts a unit.
func (m *Manager) Restart(unit string) error {
	args := append(m.systemctlArgs(), "restart", unit)
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		if strings.Contains(stderr, "not found") {
			return fmt.Errorf("unit %q not found", unit)
		}
		return fmt.Errorf("restart %q failed: %s", unit, strings.TrimSpace(stderr))
	}
	return err
}

// DaemonReload reloads the systemd daemon.
func (m *Manager) DaemonReload() error {
	args := append(m.systemctlArgs(), "daemon-reload")
	_, stderr, exitCode, err := m.r.Run(args[0], args[1:]...)

	if exitCode != 0 {
		return fmt.Errorf("daemon-reload failed: %s", strings.TrimSpace(stderr))
	}
	return err
}

// IsActive checks if a unit is active.
func (m *Manager) IsActive(unit string) bool {
	args := append(m.systemctlArgs(), "is-active", "--quiet", unit)
	_, _, exitCode, _ := m.r.Run(args[0], args[1:]...)
	return exitCode == 0
}

// Status checks the status of a unit, returning error if not active.
func (m *Manager) Status(unit string) error {
	if !m.IsActive(unit) {
		return fmt.Errorf("unit %q is not active", unit)
	}
	return nil
}
