package repo

import "sync"

// fakeHeadReconciler is a HeadReconciler test double that records every
// call, in the exact order it happened, so tests can assert both that a
// step ran and where it ran relative to the others.
type fakeHeadReconciler struct {
	mu    sync.Mutex
	Calls []string

	ReconcileClaudeErr error
	ReconcileCursorErr error

	ClaudeUnitStateFunc  func(name string) (bool, bool, error)
	DisableClaudeUnitErr map[string]error

	CursorUnitStateFunc  func(name string) (bool, bool, error)
	DisableCursorUnitErr map[string]error

	ClaudeHeadEnabledVal bool
	ClaudeHeadEnabledErr error

	CursorHeadEnabledVal bool
	CursorHeadEnabledErr error

	CodexActiveVal bool
	CodexActiveErr error

	CodexEnabledStateVal string
	CodexEnabledStateErr error
}

func newFakeHeadReconciler() *fakeHeadReconciler {
	return &fakeHeadReconciler{
		DisableClaudeUnitErr: map[string]error{},
		DisableCursorUnitErr: map[string]error{},
	}
}

func (f *fakeHeadReconciler) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, call)
}

func (f *fakeHeadReconciler) ReconcileClaude() error {
	f.record("ReconcileClaude")
	return f.ReconcileClaudeErr
}

func (f *fakeHeadReconciler) ClaudeUnitState(name string) (bool, bool, error) {
	f.record("ClaudeUnitState:" + name)
	if f.ClaudeUnitStateFunc != nil {
		return f.ClaudeUnitStateFunc(name)
	}
	return false, false, nil
}

func (f *fakeHeadReconciler) DisableClaudeUnit(name string) error {
	f.record("DisableClaudeUnit:" + name)
	return f.DisableClaudeUnitErr[name]
}

func (f *fakeHeadReconciler) ClaudeHeadEnabled() (bool, error) {
	f.record("ClaudeHeadEnabled")
	return f.ClaudeHeadEnabledVal, f.ClaudeHeadEnabledErr
}

func (f *fakeHeadReconciler) CodexActive() (bool, error) {
	f.record("CodexActive")
	return f.CodexActiveVal, f.CodexActiveErr
}

func (f *fakeHeadReconciler) CodexEnabledState() (string, error) {
	f.record("CodexEnabledState")
	return f.CodexEnabledStateVal, f.CodexEnabledStateErr
}

func (f *fakeHeadReconciler) CursorUnitState(name string) (bool, bool, error) {
	f.record("CursorUnitState:" + name)
	if f.CursorUnitStateFunc != nil {
		return f.CursorUnitStateFunc(name)
	}
	return false, false, nil
}

func (f *fakeHeadReconciler) DisableCursorUnit(name string) error {
	f.record("DisableCursorUnit:" + name)
	return f.DisableCursorUnitErr[name]
}

func (f *fakeHeadReconciler) CursorHeadEnabled() (bool, error) {
	f.record("CursorHeadEnabled")
	return f.CursorHeadEnabledVal, f.CursorHeadEnabledErr
}

func (f *fakeHeadReconciler) ReconcileCursor() error {
	f.record("ReconcileCursor")
	return f.ReconcileCursorErr
}

var _ HeadReconciler = (*fakeHeadReconciler)(nil)

// fakeHeads are the Claude and Codex repo heads over one fake, in provider
// order, as production builds them over the real heads when cursor is not selected.
func fakeHeads(f *fakeHeadReconciler) []Head {
	return []Head{ClaudeRepoHead("claude", f), CodexRepoHead("codex", f)}
}

// fakeHeadsWithCursor are the Claude, Cursor and Codex repo heads over one fake,
// for testing cursor-selected scenarios.
