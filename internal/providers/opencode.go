package providers

import (
	"fmt"
	"path/filepath"

	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// opencodeRunnerFactory creates runners for OpenCode login verification.
// Can be overridden in tests.
var opencodeRunnerFactory = func() runner.Runner { return runner.NewExecRunner() }

// The OpenCode CLI: a coding CLI whose host head runs one host-wide
// opencode-server.service (--user systemd unit) on localhost:4096 with a
// random password. Access is via web UI or SSH tunnel + opencode attach.
// OpenCode server runs as the operator and can access repos the operator can access.

const opencodeName = "opencode"
const opencodeBinary = "opencode"

func opencode() *Provider {
	return &Provider{
		Name: opencodeName,
		Kind: CodingCLI,
		Capabilities: Capabilities{
			Install: Yes, Configure: Yes, Auth: Yes, Update: Yes,
			Remove: No, Service: Yes, NativeRemote: No,
		},
		Binary:          opencodeBinary,
		ExecutablePaths: []string{".opencode/bin/opencode"},
		Label:           "OpenCode",
		// OpenCode does not persist credentials in a checked file; login state is
		// verified by opencode auth list. This is a placeholder Credential.
		Credential: &Credential{
			Path:   ".opencode/auth.json",
			Stored: func(path string) bool { return false }, // Login is verified via auth list
		},
		Login: &Login{
			Summary: "Authenticate OpenCode",
			Command: login.CLI{
				Binary:          opencodeBinary,
				ExecutablePaths: []string{".opencode/bin/opencode"},
				Args:            []string{"auth", "login"},
				// After verifies the login succeeded by checking opencode auth list.
				After: func(home string) error {
					opencodeBin := filepath.Join(home, ".opencode", "bin", opencodeBinary)
					stdout, _, _, err := opencodeRunnerFactory().Run(opencodeBin, "auth", "list")
					if err != nil {
						return fmt.Errorf("OpenCode login verification failed: %v", err)
					}
					// Check if there's any auth entry in the output (non-empty list).
					// The output format varies; for now, just verify the command succeeded.
					if stdout == "" {
						return fmt.Errorf("run codvps login opencode first")
					}
					return nil
				},
			},
		},
		Head: &Head{
			Host: func(h *head.Heads) head.Host { return h.OpenCode },
			// No Repo: the OpenCode server is host-wide, so repo add/remove
			// have nothing to attach or detach.
		},
		Doctor: func(h *head.Heads) doctor.Suite { return doctor.OpenCodeSuite(opencodeName, opencodeBinary) },
		Provision: &Provision{
			Title: "OpenCode CLI (https://opencode.ai/install) — headless server on 127.0.0.1:4096; access via web UI or opencode attach over SSH tunnel",
			// Official installer installs to ~/.opencode/bin/opencode
			Present: []string{"~/.opencode/bin/" + opencodeBinary},
			Run: func(p Provisioner) error {
				return p.AsOperator(nil, "curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path")
			},
			AsOperator: true,
			Update: func(p Provisioner, bin string) error {
				return p.AsOperator(nil, shellQuote(bin)+" upgrade")
			},
			Restart: Restart{User: true, Unit: func(string) string { return systemd.OpenCodeServerTemplate }},
		},
		Install: &Install{
			Units: systemd.OpenCodeUserUnits,
			Detect: func(h InstallHost) bool {
				return isExecutable(filepath.Join(h.Operator().Home, ".opencode", "bin", opencodeBinary))
			},
			Stop: opencodeStop,
			Kept: func(home string) []string {
				return []string{filepath.Join(home, ".opencode") + " (OpenCode configuration)"}
			},
		},
	}
}

// opencodeStop stops and disables the host-wide opencode-server service.
func opencodeStop(h UninstallHost) {
	h.Printf("--- opencode-server.service (user unit) ---\n")
	// Check if the unit is enabled or active
	output := h.UserOutput("list-unit-files", "opencode-server.service", "--no-legend", "--no-pager")
	if output == "" {
		h.Printf("opencode-server.service: not installed for %s.\n", h.Operator().Name)
		return
	}
	if h.DryRun() {
		h.Printf("DRY-RUN: would stop and disable opencode-server.service\n")
		return
	}
	if err := h.RunUser("disable", "--now", "opencode-server.service"); err != nil {
		h.Warnf("failed to stop/disable opencode-server.service: %v; continuing", err)
		return
	}
	h.Printf("stopped and disabled opencode-server.service\n")
}
