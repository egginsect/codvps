package systemd

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

// unitLines returns the exact (trimmed) non-empty lines of a unit file.
func unitLines(t *testing.T, content []byte) []string {
	t.Helper()
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(content))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan unit: %v", err)
	}
	return lines
}

func hasLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func claudeUnit(t *testing.T, name string) UnitTemplate {
	t.Helper()
	for _, u := range ClaudeUserUnits() {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no embedded unit named %s", name)
	return UnitTemplate{}
}

// Reference scenario test_unit_installed + test_restart_always: the
// service template scrubs provider credentials from its environment and
// restarts unconditionally.
func TestClaudeRemoteServiceTemplate_ScrubsCredentialsAndRestartsAlways(t *testing.T) {
	u := claudeUnit(t, ClaudeRemoteTemplate)
	if u.InstallPath != "/etc/systemd/user/claude-remote@.service" {
		t.Fatalf("install path = %q", u.InstallPath)
	}
	lines := unitLines(t, u.Content)
	for _, want := range []string{
		"UnsetEnvironment=ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL",
		"Restart=always",
		"RestartSec=5",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin",
		"WorkingDirectory=%h/%i",
		"After=network-online.target",
		"Wants=network-online.target",
		"WantedBy=default.target",
	} {
		if !hasLine(lines, want) {
			t.Errorf("claude-remote@.service is missing the line %q", want)
		}
	}
}

// Reference scenarios test_remote_control_invocation and
// test_claude_is_never_launched_interactively: the head runs exactly
// `claude remote-control` in worktree mode with a per-repository session
// prefix and never opens an interactive session in the checkout.
func TestClaudeRemoteServiceTemplate_RemoteControlExecStartIsNeverInteractive(t *testing.T) {
	lines := unitLines(t, claudeUnit(t, ClaudeRemoteTemplate).Content)
	want := "ExecStart=/usr/local/bin/codvps internal shell-exec --provider claude -- claude remote-control --spawn worktree --capacity 4 --no-create-session-in-dir --remote-control-session-name-prefix cloud-%i"
	var execStarts []string
	for _, l := range lines {
		if strings.HasPrefix(l, "ExecStart") {
			execStarts = append(execStarts, l)
		}
	}
	if len(execStarts) != 1 || execStarts[0] != want {
		t.Fatalf("ExecStart lines = %q, want exactly [%q]", execStarts, want)
	}
}

// Reference scenario test_claude_remote_slice_budget: every instance is
// placed in the one host-wide slice, and that slice carries the budget.
func TestClaudeRemoteSliceTemplate_HostWideBudget(t *testing.T) {
	if !hasLine(unitLines(t, claudeUnit(t, ClaudeRemoteTemplate).Content), "Slice=claude-remote.slice") {
		t.Fatalf("claude-remote@.service is not placed in claude-remote.slice")
	}
	u := claudeUnit(t, ClaudeRemoteSlice)
	if u.InstallPath != "/etc/systemd/user/claude-remote.slice" {
		t.Fatalf("install path = %q", u.InstallPath)
	}
	lines := unitLines(t, u.Content)
	for _, want := range []string{"[Slice]", "MemoryHigh=4G", "MemoryMax=5G", "CPUQuota=300%"} {
		if !hasLine(lines, want) {
			t.Errorf("claude-remote.slice is missing the line %q", want)
		}
	}
}

func TestClaudeUserUnits_SliceFirstAndCopiesContent(t *testing.T) {
	units := ClaudeUserUnits()
	if len(units) != 2 || units[0].Name != ClaudeRemoteSlice || units[1].Name != ClaudeRemoteTemplate {
		t.Fatalf("unexpected unit order: %+v", units)
	}
	units[0].Content[0] = 'X'
	if ClaudeUserUnits()[0].Content[0] == 'X' {
		t.Fatalf("ClaudeUserUnits exposed the embedded template for mutation")
	}
}

func cursorUnit(t *testing.T, name string) UnitTemplate {
	t.Helper()
	for _, u := range CursorUserUnits() {
		if u.Name == name {
			return u
		}
	}
	t.Fatalf("no embedded unit named %s", name)
	return UnitTemplate{}
}

// Cursor head: cursor-remote@<repo>.service template has absolute paths,
// not home-relative ones, and uses %H (hostname) for uniqueness, not %h (home).
func TestCursorRemoteServiceTemplate_AbsolutePathsAndHostnameUniqueness(t *testing.T) {
	u := cursorUnit(t, CursorRemoteTemplate)
	if u.InstallPath != "/etc/systemd/user/cursor-remote@.service" {
		t.Fatalf("install path = %q", u.InstallPath)
	}
	lines := unitLines(t, u.Content)
	for _, want := range []string{
		"WorkingDirectory=%h/%i",
		"ExecStart=/usr/local/bin/codvps internal shell-exec --provider cursor -- %h/.local/bin/agent worker --name %H-%i --worker-dir %h/%i --data-dir %h/.local/share/cursor-agent/codvps-workers/%i start",
		"Restart=always",
		"RestartSec=5",
		"WantedBy=default.target",
	} {
		if !hasLine(lines, want) {
			t.Errorf("cursor-remote@.service is missing the line %q", want)
		}
	}
	// Verify no relative home paths
	for _, l := range lines {
		if strings.Contains(l, "~/") {
			t.Errorf("cursor-remote@.service has relative home path: %q", l)
		}
	}
	// Verify hostname-based uniqueness
	if !hasLine(lines, "ExecStart=/usr/local/bin/codvps internal shell-exec --provider cursor -- %h/.local/bin/agent worker --name %H-%i --worker-dir %h/%i --data-dir %h/.local/share/cursor-agent/codvps-workers/%i start") {
		t.Errorf("cursor-remote@.service worker name must use %%H (hostname), not %%h (home)")
	}
}

func TestCursorUserUnits_CopiesContent(t *testing.T) {
	units := CursorUserUnits()
	if len(units) != 1 || units[0].Name != CursorRemoteTemplate {
		t.Fatalf("unexpected unit: %+v", units)
	}
	units[0].Content[0] = 'X'
	if CursorUserUnits()[0].Content[0] == 'X' {
		t.Fatalf("CursorUserUnits exposed the embedded template for mutation")
	}
}

// Every head that runs in the operator's login-shell environment starts
// through `codvps internal shell-exec --provider <name> -- <command>`; the
// unit's own Environment= remains the fallback when the shell is
// unavailable.
func TestHeadUnitsStartThroughShellExec(t *testing.T) {
	for _, tc := range []struct {
		unit     UnitTemplate
		provider string
		command  string
	}{
		{claudeUnit(t, ClaudeRemoteTemplate), "claude", "claude remote-control "},
		{CodexSystemUnits()[0], "codex", "/usr/local/bin/codvps internal codex-remote-start"},
		{cursorUnit(t, CursorRemoteTemplate), "cursor", "%h/.local/bin/agent worker "},
		{OpenCodeUserUnits()[0], "opencode", "%h/.opencode/bin/opencode serve --hostname 127.0.0.1 --port 4096"},
	} {
		prefix := "ExecStart=/usr/local/bin/codvps internal shell-exec --provider " + tc.provider + " -- " + tc.command
		var execStarts []string
		for _, l := range unitLines(t, tc.unit.Content) {
			if strings.HasPrefix(l, "ExecStart") {
				execStarts = append(execStarts, l)
			}
		}
		if len(execStarts) != 1 || !strings.HasPrefix(execStarts[0], prefix) {
			t.Errorf("%s ExecStart lines = %q, want one starting %q", tc.unit.Name, execStarts, prefix)
		}
		if !hasLinePrefix(unitLines(t, tc.unit.Content), "Environment") {
			t.Errorf("%s lost its fallback Environment=", tc.unit.Name)
		}
	}
}

func hasLinePrefix(lines []string, prefix string) bool {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}
