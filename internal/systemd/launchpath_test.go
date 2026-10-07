package systemd

import (
	"strings"
	"testing"
)

func TestLaunchPATHIsTheUnitTemplatesEnvironmentPATH(t *testing.T) {
	for _, tc := range []struct{ unit, want string }{
		{"claude-remote@hello.service", "/usr/local/bin:/usr/bin:/bin"},
		{"codex-remote@op.service", "/usr/local/bin:/usr/bin:/bin"},
		{"cursor-remote@hello.service", "/home/op/.local/bin:/usr/local/bin:/usr/bin:/bin"},
		{"opencode-server.service", "/home/op/.opencode/bin:/usr/local/bin:/usr/bin:/bin"},
	} {
		got, err := LaunchPATH(tc.unit, "/home/op", "op")
		if err != nil || got != tc.want {
			t.Errorf("LaunchPATH(%s) = %q, %v; want %q", tc.unit, got, err, tc.want)
		}
	}
	if _, err := LaunchPATH("sshd.service", "/home/op", "op"); err == nil {
		t.Error("a unit codvps does not ship must be an error")
	}
}

func TestEveryShellExecHeadUnitDeclaresEnvironmentPATH(t *testing.T) {
	var units []UnitTemplate
	for _, g := range [][]UnitTemplate{ClaudeUserUnits(), CodexSystemUnits(), CursorUserUnits(), OpenCodeUserUnits()} {
		units = append(units, g...)
	}
	n := 0
	for _, u := range units {
		c := string(u.Content)
		if !strings.Contains(c, "internal shell-exec") {
			continue
		}
		n++
		if !strings.Contains(c, "\nEnvironment=PATH=") && !strings.Contains(c, "\nEnvironment=\"PATH=") {
			t.Errorf("%s runs shell-exec without Environment=PATH", u.InstallPath)
		}
	}
	if n != 4 {
		t.Errorf("checked %d shell-exec units, want 4", n)
	}
}
