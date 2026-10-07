package systemd

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// The product systemd --user templates for the Claude head. They
// are embedded rather than read from disk so the single static binary is
// the only artifact install needs to write them, and so the
// fake-VPS harness and the unit tests below install/inspect exactly the
// bytes that ship.
//
// Semantics carried over from the reference unit files:
//   - one claude-remote@<repo>.service instance per registered repository,
//     running `claude remote-control` from ~/<repo> and never an interactive
//     session (--no-create-session-in-dir);
//   - Restart=always with RestartSec=5, so a killed head comes back on its
//     own;
//   - every instance lives in claude-remote.slice, one host-wide budget
//     (MemoryHigh=4G, MemoryMax=5G, CPUQuota=300%) shared by all heads;
//   - the API-key/OAuth-token/base-URL variables are scrubbed so a head can
//     only use the operator's stored Claude login, and PATH is fixed so the
//     unit does not depend on a login shell.

//go:embed units/claude-remote@.service
var claudeRemoteServiceTemplate []byte

//go:embed units/claude-remote.slice
var claudeRemoteSliceTemplate []byte

// ClaudeRemoteTemplate is the claude-remote@.service template unit name.
const ClaudeRemoteTemplate = "claude-remote@.service"

// ClaudeRemoteSlice is the shared claude-remote.slice unit name.
const ClaudeRemoteSlice = "claude-remote.slice"

// UserUnitDir is where system-wide systemd --user units live; every user's
// manager searches it, which is what lets one template serve the operator
// without writing into their home directory.
const UserUnitDir = "/etc/systemd/user"

// SystemUnitDir is where system-scope units live. The Codex head stays a
// system unit (User=%i) so enabling, disabling and the watchdog keep going
// through the same sudo systemctl calls; it needs no sandbox.
const SystemUnitDir = "/etc/systemd/system"

// CodvpsBinary is the installed codvps executable. Units name this fixed
// root-owned path, never whatever codvps the operator's PATH resolves.
const CodvpsBinary = "/usr/local/bin/codvps"

// The Codex head: one codex-remote@<operator>.service system
// instance, and a --user watchdog timer that runs `codvps head ensure codex`.
//
// Semantics carried over from the reference unit files:
//   - Type=oneshot + RemainAfterExit=yes: ExecStart (codvps internal
//     codex-remote-start) starts the Codex Remote daemon and exits, so there
//     is deliberately no Restart=; recovery is explicit (head enable/ensure);
//   - no sandbox: the unit runs the ordinary Codex daemon for
//     ~/.codex as the operator, shared with the interactive codex CLI;
//   - the watchdog is a --user timer (OnBootSec=10s, OnUnitActiveSec=60s)
//     enabled and disabled together with the head.

//go:embed units/codex-remote@.service
var codexRemoteServiceTemplate []byte

//go:embed units/codex-remote-watchdog.service
var codexWatchdogServiceTemplate []byte

//go:embed units/codex-remote-watchdog.timer
var codexWatchdogTimerTemplate []byte

//go:embed units/cursor-remote@.service
var cursorRemoteServiceTemplate []byte

// CodexRemoteTemplate is the codex-remote@.service template unit name.
const CodexRemoteTemplate = "codex-remote@.service"

// CodexWatchdogService is the watchdog's oneshot --user service.
const CodexWatchdogService = "codex-remote-watchdog.service"

// CodexWatchdogTimer is the watchdog's --user timer, the unit the Codex
// head enables and disables.
const CodexWatchdogTimer = "codex-remote-watchdog.timer"

// UnitTemplate is one product unit file: its unit name, where install puts
// it, and its exact contents.
type UnitTemplate struct {
	Name        string
	InstallPath string
	Content     []byte
}

// ClaudeUserUnits returns the Claude head's systemd --user unit files in
// install order (the slice first, so the service's Slice= reference
// resolves on the first daemon-reload). Content is a fresh copy each call,
// so a caller cannot mutate the embedded template.
func ClaudeUserUnits() []UnitTemplate {
	return []UnitTemplate{
		{
			Name:        ClaudeRemoteSlice,
			InstallPath: filepath.Join(UserUnitDir, ClaudeRemoteSlice),
			Content:     append([]byte(nil), claudeRemoteSliceTemplate...),
		},
		{
			Name:        ClaudeRemoteTemplate,
			InstallPath: filepath.Join(UserUnitDir, ClaudeRemoteTemplate),
			Content:     append([]byte(nil), claudeRemoteServiceTemplate...),
		},
	}
}

// CodexSystemUnits returns the Codex head's system unit files.
func CodexSystemUnits() []UnitTemplate {
	return []UnitTemplate{
		{
			Name:        CodexRemoteTemplate,
			InstallPath: filepath.Join(SystemUnitDir, CodexRemoteTemplate),
			Content:     append([]byte(nil), codexRemoteServiceTemplate...),
		},
	}
}

// CodexWatchdogUserUnits returns the Codex watchdog's systemd --user unit
// files in install order (the service first, so the timer's implicit
// Unit= resolves on the first daemon-reload).
func CodexWatchdogUserUnits() []UnitTemplate {
	return []UnitTemplate{
		{
			Name:        CodexWatchdogService,
			InstallPath: filepath.Join(UserUnitDir, CodexWatchdogService),
			Content:     append([]byte(nil), codexWatchdogServiceTemplate...),
		},
		{
			Name:        CodexWatchdogTimer,
			InstallPath: filepath.Join(UserUnitDir, CodexWatchdogTimer),
			Content:     append([]byte(nil), codexWatchdogTimerTemplate...),
		},
	}
}

// CodexUnit is the Codex head's system instance for operator.
func CodexUnit(operator string) string {
	return "codex-remote@" + operator + ".service"
}

// StaleDropIn is a drop-in file an earlier codvps generated and no longer
// does. Owns recognises those bytes, so install removes only what codvps
// wrote and never a drop-in the operator added.
type StaleDropIn struct {
	Path string
	Owns func(content []byte) bool
}

// CodexWorkspacesDropIn is the workspaces.conf drop-in the retired Codex
// mount-namespace sandbox generated: a [Service] section of only
// BindPaths=/home/<operator>/<repo> and
// TemporaryFileSystem=/home/<operator>/<repo>/.claude/worktrees:ro lines.
func CodexWorkspacesDropIn(operator string) StaleDropIn {
	repo := `[A-Za-z0-9][A-Za-z0-9._-]*`
	home := regexp.QuoteMeta("/home/" + operator + "/")
	line := regexp.MustCompile(`^(BindPaths=` + home + repo + `|TemporaryFileSystem=` + home + repo + `/\.claude/worktrees:ro)$`)
	return StaleDropIn{
		Path: filepath.Join(SystemUnitDir, CodexUnit(operator)+".d", "workspaces.conf"),
		Owns: func(content []byte) bool {
			lines := strings.Split(string(content), "\n")
			if len(lines) < 2 || lines[0] != "[Service]" || lines[len(lines)-1] != "" {
				return false
			}
			for _, l := range lines[1 : len(lines)-1] {
				if !line.MatchString(l) {
					return false
				}
			}
			return true
		},
	}
}

// The Cursor head: one cursor-remote@<repo>.service --user instance
// per registered repository.
//
// Semantics:
//   - one cursor-remote@<repo>.service instance per registered repository,
//     running `agent worker start` from ~/<repo>;
//   - Restart=always with RestartSec=5, so a killed head comes back on its own;
//   - the agent binary is at ~/.local/bin/agent and found via PATH;
//   - worker name is %H-%i (hostname-repository) for unique identification per host.

// CursorRemoteTemplate is the cursor-remote@.service template unit name.
const CursorRemoteTemplate = "cursor-remote@.service"

// CursorUserUnits returns the Cursor head's systemd --user unit files.
func CursorUserUnits() []UnitTemplate {
	return []UnitTemplate{
		{
			Name:        CursorRemoteTemplate,
			InstallPath: filepath.Join(UserUnitDir, CursorRemoteTemplate),
			Content:     append([]byte(nil), cursorRemoteServiceTemplate...),
		},
	}
}

// The OpenCode head: one host-wide opencode-server.service --user
// unit that provides a headless server on 127.0.0.1:4096.
//
// Semantics:
//   - one opencode-server.service per host (not per repo);
//   - Restart=always with RestartSec=5, so a killed server comes back on its own;
//   - the opencode binary is at ~/.local/bin/opencode;
//   - the server password is stored in ~/.config/codvps/opencode-password (0600).

//go:embed units/opencode-server.service
var opencodeServerServiceTemplate []byte

// OpenCodeServerTemplate is the opencode-server.service template unit name.
const OpenCodeServerTemplate = "opencode-server.service"

// OpenCodeUserUnits returns the OpenCode head's systemd --user unit files.
func OpenCodeUserUnits() []UnitTemplate {
	return []UnitTemplate{
		{
			Name:        OpenCodeServerTemplate,
			InstallPath: filepath.Join(UserUnitDir, OpenCodeServerTemplate),
			Content:     append([]byte(nil), opencodeServerServiceTemplate...),
		},
	}
}

// LaunchPATH is the PATH the named head unit starts its launcher with: the
// Environment=PATH= of the embedded unit template, with %h, %i and %% as
// systemd expands them for home and instance (the operator). It is an
// error for a unit that is not one of the embedded heads or sets no PATH:
// the launch PATH is never guessed.
func LaunchPATH(unit, home, instance string) (string, error) {
	name := unit
	if at := strings.Index(unit, "@"); at >= 0 {
		name = unit[:at] + "@.service"
	}
	templates := map[string][]byte{
		ClaudeRemoteTemplate:   claudeRemoteServiceTemplate,
		CodexRemoteTemplate:    codexRemoteServiceTemplate,
		CursorRemoteTemplate:   cursorRemoteServiceTemplate,
		OpenCodeServerTemplate: opencodeServerServiceTemplate,
	}
	content, found := templates[name]
	if !found {
		return "", fmt.Errorf("%s is not a codvps head unit", unit)
	}
	path := ""
	for _, line := range strings.Split(string(content), "\n") {
		v, isEnv := strings.CutPrefix(strings.TrimSpace(line), "Environment=")
		if !isEnv {
			continue
		}
		if p, isPath := strings.CutPrefix(strings.Trim(v, `"`), "PATH="); isPath {
			path = p
		}
	}
	if path == "" {
		return "", fmt.Errorf("%s template sets no Environment=PATH", name)
	}
	return strings.NewReplacer("%h", home, "%i", instance, "%%", "%").Replace(path), nil
}
