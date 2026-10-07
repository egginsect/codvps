package head

import (
	"fmt"
	"strings"
	"testing"
)

// fakeCodex is a CodexView with fixed answers.
type fakeCodex struct {
	enabled   string
	running   bool
	reason    string
	active    string
	sub       string
	returnErr error
}

func (f fakeCodex) EnabledState() (string, error) { return f.enabled, f.returnErr }
func (f fakeCodex) DaemonStatus() (bool, string, error) {
	return f.running, f.reason, f.returnErr
}
func (f fakeCodex) UnitState() (string, string, error) {
	return f.active, f.sub, f.returnErr
}

func (f fakeCodex) SetEnabled(bool) error         { return nil }
func (f fakeCodex) ListRows() ([]ListRow, error)  { return codexListRows(f) }
func (f fakeCodex) StatusRow() (StatusRow, error) { return codexStatusRow(f) }

func row(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// testHeads are the two production heads in provider order.
func testHeads(claude *Claude, codex Host) []Named {
	return []Named{{Name: "claude", Host: claude}, {Name: "codex", Host: codex}}
}

// testInteractive are the production coding CLIs' interactive tools.
var testInteractive = []Tool{{Label: "Claude", Binary: "claude"}, {Label: "Codex", Binary: "codex"}}

// Reference head_list: each head's rows in provider order -- the claude
// host row, registered repositories and any orphaned instance systemd
// still reports, then the codex host row.
func TestRenderList_ExactTable(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha", "beta")
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	h.sd.units["claude-remote@orphan.service"] = &fakeUnit{loaded: true}

	var out strings.Builder
	if err := RenderList(&out, testHeads(h.claude, fakeCodex{enabled: "", running: false})); err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	want := row(headListRow, "HEAD", "SCOPE", "TARGET", "ENABLED", "ACTIVE") +
		row(headListRow, "claude", "host", "-", "enabled", "-") +
		row(headListRow, "claude", "repo", "alpha", "enabled", "active") +
		row(headListRow, "claude", "repo", "beta", "enabled", "active") +
		row(headListRow, "claude", "repo", "orphan", "disabled", "inactive") +
		row(headListRow, "codex", "host", "-", "unknown", "unavailable")
	if out.String() != want {
		t.Fatalf("head list =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestRenderList_HeadDisabledAndCodexRunning(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")
	var out strings.Builder
	if err := RenderList(&out, testHeads(h.claude, fakeCodex{enabled: "enabled", running: true})); err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	want := row(headListRow, "HEAD", "SCOPE", "TARGET", "ENABLED", "ACTIVE") +
		row(headListRow, "claude", "host", "-", "disabled", "-") +
		row(headListRow, "claude", "repo", "alpha", "disabled", "inactive") +
		row(headListRow, "codex", "host", "-", "enabled", "running")
	if out.String() != want {
		t.Fatalf("head list =\n%s\nwant\n%s", out.String(), want)
	}
}

// A listing failure is reported, not hidden, but the registered
// repositories are still shown.
func TestRenderList_WarnsWhenInstancesCannotBeListed(t *testing.T) {
	h := newTestHost(t)
	h.register("alpha")
	h.sd.fail["list-units --no-pager"] = h.busFailure()
	var out strings.Builder
	if err := RenderList(&out, testHeads(h.claude, fakeCodex{})); err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	if !strings.Contains(h.diag.String(), "warning") || !strings.Contains(h.diag.String(), "Failed to connect to bus") {
		t.Fatalf("listing failure not reported: %q", h.diag.String())
	}
	if !strings.Contains(out.String(), "alpha") {
		t.Fatalf("registered repository missing: %s", out.String())
	}
}

func TestRenderList_PropagatesCodexErrors(t *testing.T) {
	h := newTestHost(t)
	if err := RenderList(&strings.Builder{}, testHeads(h.claude, fakeCodex{returnErr: errString("codex probe failed")})); err == nil {
		t.Fatal("a Codex probe error was swallowed")
	}
}

// Reference status_report: versions and linger, host heads, then
// per-repository unit state and restart count.
func TestRenderStatus_ExactReport(t *testing.T) {
	h := newTestHost(t)
	h.register("hello")
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v", err)
	}
	h.sd.units["claude-remote@hello.service"].restarts = 2
	h.sd.units["claude-remote@leftover.service"] = &fakeUnit{loaded: true}

	var out strings.Builder
	err := RenderStatus(&out, StatusOptions{
		Heads:       testHeads(h.claude, fakeCodex{active: "active", sub: "exited", running: false, reason: "no listening app-server control socket found (looked for: /x.sock)"}),
		Interactive: testInteractive,
		Tools:       h.sd,
		Operator:    "operator",
	})
	if err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	want := "Linger: yes\n" +
		"Node: node 1.2.3\n" +
		"Claude (interactive): claude 1.2.3\n" +
		"Codex (interactive): codex 1.2.3\n" +
		"GitHub CLI: gh 1.2.3\n" +
		"\n" + row(statusRow, "HOST HEAD", "UNIT", "SUBSTATE", "DAEMON") +
		row(statusRow, "claude", "enabled", "-", "-") +
		row(statusRow, "codex", "active", "exited", "unavailable") +
		"  (daemon unreachable from this session: no listening app-server control socket found (looked for: /x.sock))\n" +
		"\n" + row(statusRow, "REPOSITORY", "ACTIVE", "SUBSTATE", "RESTARTS") +
		row(statusRow, "hello", "active", "running", "2") +
		row(statusRow, "leftover", "inactive", "dead", "0")
	if out.String() != want {
		t.Fatalf("status =\n%s\nwant\n%s", out.String(), want)
	}
	if len(h.sd.callsMatching("loginctl", "show-user", "operator", "-p", "Linger", "--value")) != 1 {
		t.Fatalf("linger was not probed for the operator: %v", h.sd.calls)
	}
}

// A read-only report degrades to the reference's fallback labels instead of
// failing when tools or the manager cannot answer.
func TestRenderStatus_FallbacksWhenProbesFail(t *testing.T) {
	h := newTestHost(t)
	h.register("hello")
	h.sd.fail["show claude-remote@hello.service"] = h.busFailure()
	var out strings.Builder
	err := RenderStatus(&out, StatusOptions{Heads: testHeads(h.claude, fakeCodex{}), Interactive: testInteractive, Tools: failingTools{}, Operator: "operator"})
	if err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	for _, want := range []string{
		"Linger: unknown\n",
		"Node: unavailable\n",
		row(statusRow, "codex", "not-found", "-", "unavailable"),
		row(statusRow, "claude", "disabled", "-", "-"),
		row(statusRow, "hello", "not-found", "-", "0"),
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status is missing %q:\n%s", want, out.String())
		}
	}
}

func (h *testHost) busFailure() (r fakeResponse) {
	return fakeResponse{ExitCode: 1, Stderr: "Failed to connect to bus: No medium found", Err: errString("exit 1")}
}
