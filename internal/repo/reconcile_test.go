package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

func newFakeRunnerReconciler(t *testing.T) (*SystemdHeadReconciler, *runner.FakeRunner) {
	t.Helper()
	layout := newTestLayout(t)
	fr := runner.NewFakeRunner()
	return NewSystemdHeadReconciler(layout, fr, &stubClaudeHead{}, &stubCodexHead{}, nil), fr
}

func TestSystemdHeadReconciler_ClaudeUnitStateParsesEnabledAndActive(t *testing.T) {
	h, fr := newFakeRunnerReconciler(t)
	fr.SetResponse("systemctl", []string{"--user", "is-enabled", "claude-remote@repo1.service"}, runner.Response{Stdout: "enabled\n", ExitCode: 0})
	fr.SetResponse("systemctl", []string{"--user", "is-active", "--quiet", "claude-remote@repo1.service"}, runner.Response{ExitCode: 0})

	enabled, active, err := h.ClaudeUnitState("repo1")
	if err != nil {
		t.Fatalf("ClaudeUnitState: %v", err)
	}
	if !enabled || !active {
		t.Fatalf("ClaudeUnitState = (%v,%v), want (true,true)", enabled, active)
	}
}

func TestSystemdHeadReconciler_ClaudeUnitStateDisabledAndInactive(t *testing.T) {
	h, fr := newFakeRunnerReconciler(t)
	fr.SetResponse("systemctl", []string{"--user", "is-enabled", "claude-remote@repo1.service"}, runner.Response{Stdout: "disabled\n", ExitCode: 1, Err: errStub("exit 1")})
	fr.SetResponse("systemctl", []string{"--user", "is-active", "--quiet", "claude-remote@repo1.service"}, runner.Response{ExitCode: 3, Err: errStub("exit 3")})

	enabled, active, err := h.ClaudeUnitState("repo1")
	if err != nil {
		t.Fatalf("ClaudeUnitState: %v", err)
	}
	if enabled || active {
		t.Fatalf("ClaudeUnitState = (%v,%v), want (false,false)", enabled, active)
	}
}

func TestSystemdHeadReconciler_ClaudeUnitStateRejectsUnsafeName(t *testing.T) {
	h, _ := newFakeRunnerReconciler(t)
	if _, _, err := h.ClaudeUnitState("../etc"); err == nil {
		t.Fatalf("expected unsafe name to be rejected before building a unit string")
	}
}

func TestSystemdHeadReconciler_DisableClaudeUnitPropagatesErrors(t *testing.T) {
	h, fr := newFakeRunnerReconciler(t)
	fr.SetResponse("systemctl", []string{"--user", "disable", "claude-remote@repo1.service"}, runner.Response{ExitCode: 1, Stderr: "boom", Err: errStub("exit 1")})

	if err := h.DisableClaudeUnit("repo1"); err == nil {
		t.Fatalf("expected DisableClaudeUnit to propagate a disable failure")
	}
}

func TestSystemdHeadReconciler_ClaudeHeadEnabledReadsFlagFile(t *testing.T) {
	layout := newTestLayout(t)
	fr := runner.NewFakeRunner()
	h := NewSystemdHeadReconciler(layout, fr, &stubClaudeHead{}, &stubCodexHead{}, nil)

	enabled, err := h.ClaudeHeadEnabled()
	if err != nil || enabled {
		t.Fatalf("ClaudeHeadEnabled = (%v,%v), want (false,nil) before the flag exists", enabled, err)
	}

	if err := os.MkdirAll(layout.ConfigDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(layout.ConfigDir(), "claude-head-enabled"), nil, 0o600); err != nil {
		t.Fatalf("write flag: %v", err)
	}
	enabled, err = h.ClaudeHeadEnabled()
	if err != nil || !enabled {
		t.Fatalf("ClaudeHeadEnabled = (%v,%v), want (true,nil) once the flag exists", enabled, err)
	}
}

// stubClaudeHead stands in for internal/head.Claude, which this package
// cannot import (head builds on repo). The real Claude head's behavior,
// including repo add auto-attach, is tested in internal/head.
type stubClaudeHead struct {
	calls int
	err   error
}

func (s *stubClaudeHead) Reconcile() error {
	s.calls++
	return s.err
}

func TestSystemdHeadReconciler_ReconcileClaudeDelegatesAndPropagates(t *testing.T) {
	stub := &stubClaudeHead{err: errStub("claude head failed")}
	h := NewSystemdHeadReconciler(newTestLayout(t), runner.NewFakeRunner(), stub, &stubCodexHead{}, nil)
	if err := h.ReconcileClaude(); err == nil || !strings.Contains(err.Error(), "claude head failed") {
		t.Fatalf("ReconcileClaude error = %v, want the Claude head's error", err)
	}
	if stub.calls != 1 {
		t.Fatalf("Claude head reconciled %d times, want 1", stub.calls)
	}

	nilHead := NewSystemdHeadReconciler(newTestLayout(t), runner.NewFakeRunner(), nil, &stubCodexHead{}, nil)
	if err := nilHead.ReconcileClaude(); err == nil {
		t.Fatalf("ReconcileClaude without a Claude head must fail loudly, not no-op")
	}
}

// stubCodexHead stands in for internal/head.Codex for the same reason as
// stubClaudeHead; the real Codex head, including repo add/remove against
// it, is tested in internal/head.
type stubCodexHead struct {
	calls   []string
	err     error
	running bool
	state   string
}

func (s *stubCodexHead) DaemonRunning() (bool, error) {
	s.calls = append(s.calls, "DaemonRunning")
	return s.running, s.err
}

func (s *stubCodexHead) EnabledState() (string, error) {
	s.calls = append(s.calls, "EnabledState")
	return s.state, s.err
}

// Every Codex-facing HeadReconciler method is the injected Codex head's
// answer, errors included; nothing about Codex is decided in this package.
func TestSystemdHeadReconciler_DelegatesCodexToTheCodexHead(t *testing.T) {
	stub := &stubCodexHead{running: true, state: "enabled"}
	h := NewSystemdHeadReconciler(newTestLayout(t), runner.NewFakeRunner(), &stubClaudeHead{}, stub, nil)
	if up, err := h.CodexActive(); err != nil || !up {
		t.Fatalf("CodexActive = (%v,%v)", up, err)
	}
	if st, err := h.CodexEnabledState(); err != nil || st != "enabled" {
		t.Fatalf("CodexEnabledState = (%q,%v)", st, err)
	}
	want := "DaemonRunning EnabledState"
	if got := strings.Join(stub.calls, " "); got != want {
		t.Fatalf("Codex head calls = %s, want %s", got, want)
	}

	stub.err = errStub("codex head failed")
	if _, err := h.CodexActive(); err == nil || !strings.Contains(err.Error(), "codex head failed") {
		t.Fatalf("CodexActive = %v, want the Codex head's error", err)
	}

	nilHead := NewSystemdHeadReconciler(newTestLayout(t), runner.NewFakeRunner(), &stubClaudeHead{}, nil, nil)
	if _, err := nilHead.CodexActive(); err == nil {
		t.Fatalf("CodexActive without a Codex head must fail loudly, not no-op")
	}
}
