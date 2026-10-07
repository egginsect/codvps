package providers

import (
	"encoding/json"
	"fmt"
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

// cursorRunnerFactory creates runners for Cursor login verification.
// Can be overridden in tests.
var cursorRunnerFactory = func() runner.Runner { return runner.NewExecRunner() }

// The Cursor CLI: a coding CLI whose host head runs one cursor-remote@<repo>
// Remote Control instance per registered repository as a --user systemd unit.
// Cursor plan with Cloud Agents required. Agent loop and model inference run in
// Cursor's cloud. Code and context sent to Cursor. Worker has operator account's
// full filesystem access (not sandboxed to registered repos only).

const cursorName = "cursor"
const cursorBinary = "agent"
const cursorInstallerURL = "https://cursor.com/install"

func cursor() *Provider {
	return &Provider{
		Name: cursorName,
		Kind: CodingCLI,
		Capabilities: Capabilities{
			Install: Yes, Configure: Yes, Auth: Yes, Update: Yes,
			Remove: No, Service: Yes, NativeRemote: Yes,
		},
		Binary:          cursorBinary,
		ExecutablePaths: []string{".local/bin/agent"},
		Label:           "Cursor",
		// Cursor does not persist credentials in a checked file; login state is
		// verified by agent status at enable time. This is a placeholder
		// Credential to satisfy the auth=yes validation.
		Credential: &Credential{
			Path:   ".cursor/auth.json",
			Stored: func(path string) bool { return false }, // Login is verified via agent status
		},
		Login: &Login{
			Summary: "Authenticate Cursor",
			Command: login.CLI{
				Binary:          cursorBinary,
				ExecutablePaths: []string{".local/bin/agent"},
				Args:            []string{"login"},
				Env: func(home string) []string {
					return []string{
						"NO_OPEN_BROWSER=1",
						"PATH=" + filepath.Join(home, ".local", "bin") + ":/usr/local/bin:/usr/bin:/bin",
					}
				},
				// After verifies the login succeeded by checking agent status --format json.
				// Agent binary is at ~/.local/bin/agent.
				// Returns exit code 0 even when logged out; must parse JSON.
				After: func(home string) error {
					agentBin := filepath.Join(home, ".local", "bin", cursorBinary)
					stdout, _, _, err := cursorRunnerFactory().Run(agentBin, "status", "--format", "json")
					if err != nil {
						return fmt.Errorf("cursor login verification failed: %v", err)
					}
					var statusResp struct {
						IsAuthenticated bool `json:"isAuthenticated"`
					}
					if err := json.Unmarshal([]byte(stdout), &statusResp); err != nil {
						return fmt.Errorf("cursor login verification failed to parse status: %v", err)
					}
					if !statusResp.IsAuthenticated {
						return fmt.Errorf("run codvps login cursor first")
					}
					return nil
				},
			},
		},
		Head: &Head{
			Host: func(h *head.Heads) head.Host { return h.Cursor },
			Repo: func(h *head.Heads, layout *paths.Layout, r runner.Runner) repo.Head {
				return repo.CursorRepoHead(cursorName, repo.NewSystemdHeadReconciler(layout, r, nil, nil, h.Cursor))
			},
			RepoSelectedOnly: true,
		},
		Doctor: func(h *head.Heads) doctor.Suite { return doctor.CursorSuite(cursorName, cursorBinary) },
		Provision: &Provision{
			Title: "Cursor CLI (bash installer from " + cursorInstallerURL + ") — requires Cursor plan with Cloud Agents; agent and model run in Cursor's cloud; code/context sent to Cursor; worker has full filesystem access (not sandboxed)",
			// Any Cursor agent the host has (the native installer's ~/.local/bin)
			// counts as installed.
			Present: []string{"~/.local/bin/" + cursorBinary},
			Run: func(p Provisioner) error {
				return p.AsOperator(nil, "curl -fsS "+cursorInstallerURL+" | bash")
			},
			AsOperator: true,
			Update: func(p Provisioner, bin string) error {
				return p.AsOperator(nil, shellQuote(bin)+" update")
			},
			Restart: Restart{User: true, Glob: "cursor-remote@*.service"},
		},
		Install: &Install{
			Units: systemd.CursorUserUnits,
			Detect: func(h InstallHost) bool {
				return isExecutable(filepath.Join(h.Operator().Home, ".local", "bin", cursorBinary))
			},
			Stop: cursorStop,
			Kept: func(home string) []string {
				return []string{filepath.Join(home, ".cursor") + " (Cursor agent configuration)"}
			},
		},
	}
}

// cursorStop stops and disables every cursor-remote@ instance.
func cursorStop(h UninstallHost) {
	h.Printf("--- cursor-remote@*.service (user units) ---\n")
	instances := cursorInstances(h)
	if len(instances) == 0 {
		h.Printf("cursor-remote@*.service: no user units found for %s.\n", h.Operator().Name)
		return
	}
	for _, unit := range instances {
		if h.DryRun() {
			h.Printf("DRY-RUN: would stop and disable user unit %s\n", unit)
			continue
		}
		if err := h.RunUser("disable", "--now", unit); err != nil {
			h.Warnf("failed to stop/disable %s: %v; continuing", unit, err)
			continue
		}
		h.Printf("stopped and disabled user unit %s\n", unit)
	}
}

func cursorInstances(h UninstallHost) []string {
	seen := map[string]bool{}
	listing := h.UserOutput("list-units", "cursor-remote@*.service", "--all", "--plain", "--no-legend", "--no-pager") +
		"\n" + h.UserOutput("list-unit-files", "cursor-remote@*.service", "--no-legend", "--no-pager")
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		// The template itself is not an instance.
		if !strings.HasPrefix(unit, "cursor-remote@") || !strings.HasSuffix(unit, ".service") || unit == "cursor-remote@.service" || seen[unit] {
			continue
		}
		seen[unit] = true
		out = append(out, unit)
	}
	return out
}
