package providers

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/claudeconfig"
	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// The Claude Code CLI: a coding CLI whose host head runs one
// claude-remote@<repo> Remote Control instance per registered repository,
// with a managed runtime and an OAuth login.

// claudeName is the Claude Code provider's name.
const claudeName = "claude"

// claudeBinary is the Claude Code CLI's executable.
const claudeBinary = "claude"

// claudeInstallerURL is Claude Code's native installer.
const claudeInstallerURL = "https://claude.ai/install.sh"

func claude() *Provider {
	return &Provider{
		Name: claudeName,
		Kind: CodingCLI,
		Capabilities: Capabilities{
			Install: Yes, Configure: Yes, Auth: Yes, Update: Yes,
			Remove: No, Service: Yes, NativeRemote: Yes,
		},
		Binary: claudeBinary,
		Label:  "Claude",
		// Claude Remote Control must run on the OAuth login, never on an API
		// key or a redirected base URL from the operator's shell.
		Env: &Env{Remove: []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL"}},
		// The reference's claude_credential_source_state == "regular": a
		// symlink or non-regular file is never followed.
		Credential: &Credential{Path: filepath.Join(".claude", ".credentials.json"), Stored: RegularFile},
		Provision: &Provision{
			Title:      "Claude Code CLI (native installer from " + claudeInstallerURL + ")",
			AsOperator: true,
			// Any Claude CLI the host already has counts as installed; the
			// native installer's ~/.local/bin one is the one updated.
			Present: []string{"~/.local/bin/" + claudeBinary, "/usr/local/bin/" + claudeBinary, "/usr/bin/" + claudeBinary},
			Run: func(p Provisioner) error {
				return p.AsOperator(nil, "curl -fsSL "+claudeInstallerURL+" | bash")
			},
			Update: func(p Provisioner, bin string) error {
				return p.AsOperator(nil, shellQuote(bin)+" update")
			},
			Restart: Restart{User: true, Glob: "claude-remote@*.service"},
		},
		Login: &Login{
			Summary: "Authenticate Claude Code",
			Command: login.CLI{
				Binary: claudeBinary, Args: []string{"auth", "login"},
				// On a narrow SSH terminal the login URL can be clipped, and the
				// CLI's own copy shortcut is the reliable way to get it.
				Hint: "Claude remote login: if the URL is clipped, press c to copy it; paste the browser's code here when prompted.\n",
				// Seed Remote Control consent (remoteDialogSeen) so the head
				// never stops at that one-time dialog. Workspace trust is `head
				// enable claude`'s consent act, not the login's.
				After: func(home string) error {
					if err := claudeconfig.SeedRemoteConsent(home); err != nil {
						return fmt.Errorf("claude auth login succeeded, but seeding Remote Control consent failed: %w", err)
					}
					return nil
				},
			},
		},
		Head: &Head{
			Host: func(h *head.Heads) head.Host { return h.Claude },
			Repo: func(h *head.Heads, layout *paths.Layout, r runner.Runner) repo.Head {
				return repo.ClaudeRepoHead(claudeName, repo.NewSystemdHeadReconciler(layout, r, h.Claude, nil, nil))
			},
		},
		Doctor: func(*head.Heads) doctor.Suite { return doctor.ClaudeSuite(claudeName, claudeBinary) },
		Install: &Install{
			Units: systemd.ClaudeUserUnits,
			Detect: func(h InstallHost) bool {
				return isExecutable(filepath.Join(h.Operator().Home, ".local", "bin", claudeBinary)) ||
					isExecutable(h.Sys("/usr/local/bin/"+claudeBinary)) || isExecutable(h.Sys("/usr/bin/"+claudeBinary))
			},
			Stop: claudeStop,
			Kept: func(home string) []string {
				return []string{filepath.Join(home, ".claude") + " credentials"}
			},
		},
	}
}

// claudeStop stops and disables every claude-remote@ instance the
// operator's manager knows about, loaded or merely enabled.
func claudeStop(h UninstallHost) {
	h.Printf("--- claude-remote@*.service (user units) ---\n")
	instances := claudeInstances(h)
	if len(instances) == 0 {
		h.Printf("claude-remote@*.service: no user units found for %s.\n", h.Operator().Name)
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

func claudeInstances(h UninstallHost) []string {
	seen := map[string]bool{}
	listing := h.UserOutput("list-units", "claude-remote@*.service", "--all", "--plain", "--no-legend", "--no-pager") +
		"\n" + h.UserOutput("list-unit-files", "claude-remote@*.service", "--no-legend", "--no-pager")
	var out []string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		unit := fields[0]
		// The template itself (claude-remote@.service) is not an instance.
		if !strings.HasPrefix(unit, "claude-remote@") || !strings.HasSuffix(unit, ".service") || unit == systemd.ClaudeRemoteTemplate || seen[unit] {
			continue
		}
		seen[unit] = true
		out = append(out, unit)
	}
	sort.Strings(out)
	return out
}
