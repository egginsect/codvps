package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/systemd"
)

// CursorSuite is the Cursor provider's doctor checks: its CLI, login
// status via `agent status`, and its per-repository head at the end.
func CursorSuite(name, binary string) Suite {
	var st cursorState
	return Suite{
		Prepare: func(d *Check) error {
			var err error
			st, err = d.collectCursorState()
			return err
		},
		Install: func(d *Check) {
			d.checkCursorCLI(binary)
		},
		Head: func(d *Check) { d.checkCursorHead(st) },
	}
}

// cursorState is what doctor learns about the Cursor head once and reuses.
type cursorState struct {
	// configured is true once any Cursor head is a commitment: some
	// cursor-remote@ unit is enabled or active.
	configured bool
	// units is every cursor-remote@ instance enabled or active.
	units []string
	// authenticated is true if `agent status` succeeds.
	authenticated bool
}

func (d *Check) collectCursorState() (cursorState, error) {
	st := cursorState{}
	// Every census probe is read-only; a failed listing contributes no
	// units, and the user-bus check reports an unreachable manager.
	units := map[string]bool{}
	out, _, _, _ := d.opts.Runner.Run("systemctl", "--user", "list-unit-files", "cursor-remote@*.service", "--no-legend", "--no-pager")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && isCursorInstance(f[0]) && strings.HasPrefix(f[1], "enabled") {
			units[f[0]] = true
			st.configured = true
		}
	}
	out, _, _, _ = d.opts.Runner.Run("systemctl", "--user", "list-units", "cursor-remote@*.service", "--all", "--plain", "--no-legend", "--no-pager")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && isCursorInstance(f[0]) && (f[2] == "active" || f[3] == "running") {
			units[f[0]] = true
			st.configured = true
		}
	}
	for u := range units {
		st.units = append(st.units, u)
	}
	sort.Strings(st.units)
	// Check authentication: parse agent status --format json.
	// Agent binary is at ~/.local/bin/agent; use absolute path.
	// Exit code is 0 even when logged out; must parse isAuthenticated field.
	home := d.home()
	agentBin := filepath.Join(home, ".local", "bin", "agent")
	stdout, _, _, _ := d.opts.Runner.Run(agentBin, "status", "--format", "json")
	var statusResp struct {
		IsAuthenticated bool `json:"isAuthenticated"`
	}
	if err := json.Unmarshal([]byte(stdout), &statusResp); err == nil {
		st.authenticated = statusResp.IsAuthenticated
	}
	return st, nil
}

func isCursorInstance(unit string) bool {
	return strings.HasPrefix(unit, "cursor-remote@") && strings.HasSuffix(unit, ".service") && unit != "cursor-remote@.service"
}

func (d *Check) checkCursorCLI(binary string) {
	// Try to find the binary in home-relative paths first
	home := d.home()
	candidatePaths := []string{filepath.Join(home, ".local", "bin", binary)}

	var found string
	for _, path := range candidatePaths {
		if _, err := os.Stat(path); err == nil && d.succeeds(path, "--version") {
			found = path
			break
		}
	}

	// Fall back to PATH lookup
	if found == "" {
		if _, err := d.opts.LookPath(binary); err == nil && d.succeeds(binary, "--version") {
			found = binary
		}
	}

	if found != "" {
		d.Pass("%s --version runs", binary)
	} else {
		d.Fail("%s is missing or not runnable", binary)
	}
}

// checkCursorHead covers the Cursor login via agent status, the installed
// unit, and every Cursor instance.
func (d *Check) checkCursorHead(st cursorState) {
	if st.authenticated {
		d.Pass("Cursor agent is logged in (agent status succeeds)")
	} else {
		if st.configured {
			d.Fail("Cursor agent is not logged in; run codvps login cursor")
		} else {
			d.Warn("Cursor agent is not logged in; run codvps login cursor before enabling a Cursor head")
		}
	}

	unitFile := filepath.Join(systemd.UserUnitDir, "cursor-remote@.service")
	if fileExists(d.sys(unitFile)) {
		d.Pass("cursor-remote@.service unit template is installed")
	} else {
		d.Fail("cursor-remote@.service unit template is missing at %s", unitFile)
	}

	for _, unit := range st.units {
		name := strings.TrimSuffix(strings.TrimPrefix(unit, "cursor-remote@"), ".service")
		switch {
		case registry.ValidateName(name) != nil:
			d.Warn("enabled or running unit has an invalid repository instance: %s", unit)
		case repo.IsPrimaryGitCheckout(d.opts.Runner, filepath.Join(d.home(), name)) != nil:
			d.Warn("enabled or running unit %s has no repository at %s", unit, filepath.Join(d.home(), name))
		default:
			out, _, _, _ := d.opts.Runner.Run("systemctl", "--user", "is-active", unit)
			switch active := strings.TrimSpace(out); active {
			case "active":
				d.Pass("repository %s has an active Cursor unit", name)
				d.checkHeadShellPath(true, unit)
			case "failed":
				d.Fail("repository %s has a failed Cursor unit", name)
			default:
				d.Fail("repository %s has an enabled Cursor unit that is not active (%s)", name, orDefault(active, "unknown"))
			}
		}
	}
}
