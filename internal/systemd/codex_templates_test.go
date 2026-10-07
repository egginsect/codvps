package systemd

import (
	"strings"
	"testing"
)

// The product Codex unit is a oneshot system template run as the operator,
// started through the bounded-retry starter and stopped through the
// standalone Codex. It adds no sandbox of its own: the daemon must
// be the ordinary one for ~/.codex, whose control socket the host's codex
// CLI can reach.
func TestCodexRemoteServiceTemplate_HasNoSandbox(t *testing.T) {
	units := CodexSystemUnits()
	if len(units) != 1 || units[0].Name != CodexRemoteTemplate {
		t.Fatalf("CodexSystemUnits = %+v", units)
	}
	u := units[0]
	if u.InstallPath != "/etc/systemd/system/codex-remote@.service" {
		t.Fatalf("install path = %q", u.InstallPath)
	}
	lines := unitLines(t, u.Content)
	for _, want := range []string{
		"Type=oneshot",
		"RemainAfterExit=yes",
		"User=%i",
		"WorkingDirectory=/home/%i",
		"Environment=PATH=/usr/local/bin:/usr/bin:/bin",
		"ExecStart=/usr/local/bin/codvps internal shell-exec --provider codex -- /usr/local/bin/codvps internal codex-remote-start",
		"ExecStop=/home/%i/.codex/packages/standalone/current/codex remote-control stop --json",
		"WantedBy=multi-user.target",
	} {
		if !hasLine(lines, want) {
			t.Errorf("codex-remote@.service is missing %q", want)
		}
	}
	for _, l := range lines {
		for _, banned := range []string{
			"Restart=", "ExecStartPre=", "ProtectSystem=", "ProtectHome=", "PrivateTmp=", "NoNewPrivileges=",
			"RestrictSUIDSGID=", "CapabilityBoundingSet=", "BindPaths=", "BindReadOnlyPaths=", "TemporaryFileSystem=",
			"Environment=CODEX_HOME=", "Environment=HOME=",
		} {
			if strings.HasPrefix(l, banned) {
				t.Errorf("codex-remote@.service must not declare %q", l)
			}
		}
	}
	if !strings.HasPrefix(CodvpsBinary, "/usr/local/bin/") {
		t.Errorf("CodvpsBinary %q must be the root-owned install path", CodvpsBinary)
	}
}

// The watchdog is a --user timer/service pair that runs head ensure codex
// every minute, installed next to the Claude --user units.
func TestCodexWatchdogUnits(t *testing.T) {
	units := CodexWatchdogUserUnits()
	if len(units) != 2 || units[0].Name != CodexWatchdogService || units[1].Name != CodexWatchdogTimer {
		t.Fatalf("CodexWatchdogUserUnits order = %+v", units)
	}
	svc := unitLines(t, units[0].Content)
	for _, want := range []string{"Type=oneshot", "ExecStart=/usr/local/bin/codvps head ensure codex", "StandardOutput=journal"} {
		if !hasLine(svc, want) {
			t.Errorf("watchdog service is missing %q", want)
		}
	}
	timer := unitLines(t, units[1].Content)
	for _, want := range []string{"OnBootSec=10s", "OnUnitActiveSec=60s", "AccuracySec=5s", "WantedBy=timers.target"} {
		if !hasLine(timer, want) {
			t.Errorf("watchdog timer is missing %q", want)
		}
	}
	for _, u := range units {
		if u.InstallPath != "/etc/systemd/user/"+u.Name {
			t.Errorf("%s install path = %q", u.Name, u.InstallPath)
		}
	}
}

// Content is a copy: a caller cannot mutate the embedded bytes.
func TestCodexUnitsReturnCopies(t *testing.T) {
	first := CodexSystemUnits()[0].Content
	first[0] = 'X'
	if CodexSystemUnits()[0].Content[0] == 'X' {
		t.Fatalf("CodexSystemUnits exposed the embedded template")
	}
}

func TestCodexUnitNames(t *testing.T) {
	if got := CodexUnit("operator"); got != "codex-remote@operator.service" {
		t.Fatalf("CodexUnit = %q", got)
	}
}

// The retired workspaces.conf drop-in is recognised by exact path and by
// content: only the BindPaths/TemporaryFileSystem lines codvps generated.
func TestCodexWorkspacesDropIn_OwnsOnlyGeneratedContent(t *testing.T) {
	d := CodexWorkspacesDropIn("operator")
	if d.Path != "/etc/systemd/system/codex-remote@operator.service.d/workspaces.conf" {
		t.Fatalf("path = %q", d.Path)
	}
	generated := "[Service]\nBindPaths=/home/operator/repo-a\nTemporaryFileSystem=/home/operator/repo-a/.claude/worktrees:ro\n"
	for content, want := range map[string]bool{
		generated:                     true,
		"[Service]\n":                 true,
		"":                            false,
		"[Service]":                   false,
		generated + "PrivateTmp=no\n": false,
		"[Service]\nBindPaths=/home/other/repo\n":                             false,
		"[Service]\nBindPaths=/home/operator/repo-a/../../etc\n":              false,
		"[Service]\nBindPaths=/home/operator/repo-a\n# site policy\n":         false,
		"[Unit]\nBindPaths=/home/operator/repo-a\n":                           false,
		"[Service]\nTemporaryFileSystem=/home/operator/repo-a/other:ro\n": false,
	} {
		if got := d.Owns([]byte(content)); got != want {
			t.Errorf("Owns(%q) = %v, want %v", content, got, want)
		}
	}
}
