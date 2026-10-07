package repo

import (
	"fmt"
	"strings"
)

// Head is one provider's host head as the repository commands drive it.
// RepoManager iterates its heads in provider order and never names a CLI:
// repo add attaches every head, repo remove revokes, detaches and resyncs
// every head (in three phases, so a head that must be cut off first is cut
// off before anything else can fail), and repo list renders one summary
// and one column per head.
type Head interface {
	// Label names the head in messages ("Claude").
	Label() string
	// Attach reconciles the head after a repository was registered. With
	// the head disabled it must only prune.
	Attach() error
	// Attached reports whether the head still holds repository name: a
	// remove of an unregistered repository is a no-op only when no head
	// does. Errors are fully worded.
	Attached(name string) (bool, error)
	// Revoke is remove's first phase, run before any read or validation
	// that can fail: cut the head's access to every repository that may be
	// going away. notice, when not empty, is shown after a successful
	// remove. Errors are fully worded.
	Revoke() (notice string, err error)
	// Detach is remove's second phase and the vanished-checkout prune:
	// release repository name. The error is the head's own; the caller
	// words it.
	Detach(name string) error
	// Resync is remove's last phase, after the registry was rewritten. It
	// must never restart what Revoke stopped. Errors are fully worded.
	Resync(name string) error
	// Held says what stays revoked when remove fails after Revoke ("" when
	// nothing does), e.g. "Codex remains stopped".
	Held() string
	// Summary is the head's "Heads:" entry in repo list ("claude=enabled").
	Summary() (string, error)
	// Column is the head's repo list column header, and Cells its value for
	// each listed repository, in order. A head with no per-repository state
	// has an empty Column and no column is shown for it.
	Column() string
	Cells(names []string) ([]string, error)
}

// ClaudeOps are the Claude head operations the Claude repo head needs
// (SystemdHeadReconciler in production).
type ClaudeOps interface {
	ReconcileClaude() error
	ClaudeUnitState(repoName string) (enabled, active bool, err error)
	DisableClaudeUnit(repoName string) error
	ClaudeHeadEnabled() (bool, error)
}

// CodexOps are the Codex head operations the Codex repo head needs
// (SystemdHeadReconciler in production).
type CodexOps interface {
	CodexActive() (bool, error)
	CodexEnabledState() (string, error)
}

// CursorOps are the Cursor head operations the Cursor repo head needs
// (SystemdHeadReconciler in production).
type CursorOps interface {
	ReconcileCursor() error
	CursorUnitState(repoName string) (enabled, active bool, err error)
	DisableCursorUnit(repoName string) error
	CursorHeadEnabled() (bool, error)
}

// ClaudeRepoHead is the Claude head for the repository commands: one
// claude-remote@<repo> instance per repository. name is the provider's
// name.
func ClaudeRepoHead(name string, ops ClaudeOps) Head { return claudeRepoHead{name, ops} }

type claudeRepoHead struct {
	name string
	ops  ClaudeOps
}

func (h claudeRepoHead) Label() string { return "Claude" }
func (h claudeRepoHead) Attach() error { return h.ops.ReconcileClaude() }

func (h claudeRepoHead) Attached(name string) (bool, error) {
	enabled, active, err := h.ops.ClaudeUnitState(name)
	if err != nil {
		return false, fmt.Errorf("failed to query Claude head state for %q: %w", name, err)
	}
	return enabled || active, nil
}

// Revoke is a no-op: the Claude head holds each repository in its own
// unit, which Detach disables.
func (h claudeRepoHead) Revoke() (string, error) { return "", nil }

func (h claudeRepoHead) Detach(name string) error { return h.ops.DisableClaudeUnit(name) }
func (h claudeRepoHead) Resync(string) error      { return nil }
func (h claudeRepoHead) Held() string             { return "" }

func (h claudeRepoHead) Summary() (string, error) {
	enabled, err := h.ops.ClaudeHeadEnabled()
	if err != nil {
		return "", err
	}
	if enabled {
		return h.name + "=enabled", nil
	}
	return h.name + "=disabled", nil
}

func (h claudeRepoHead) Column() string { return strings.ToUpper(h.name) + "_HEAD" }

func (h claudeRepoHead) Cells(names []string) ([]string, error) {
	cells := make([]string, 0, len(names))
	for _, name := range names {
		enabled, active, err := h.ops.ClaudeUnitState(name)
		if err != nil {
			return nil, fmt.Errorf("failed to query Claude head state for %q: %w", name, err)
		}
		state := "-"
		switch {
		case active:
			state = "active"
		case enabled:
			state = "enabled"
		}
		cells = append(cells, state)
	}
	return cells, nil
}

// CodexRepoHead is the Codex head for the repository commands. Codex
// Remote is one daemon for the operator's ~/.codex that is not scoped to
// registered repositories, so registering or removing a
// repository never touches it: the head only reports its state in repo
// list. name is the provider's name.
func CodexRepoHead(name string, ops CodexOps) Head { return codexRepoHead{name, ops} }

type codexRepoHead struct {
	name string
	ops  CodexOps
}

func (h codexRepoHead) Label() string                 { return "Codex" }
func (h codexRepoHead) Attach() error                 { return nil }
func (h codexRepoHead) Attached(string) (bool, error) { return false, nil }
func (h codexRepoHead) Revoke() (string, error)       { return "", nil }
func (h codexRepoHead) Detach(string) error           { return nil }
func (h codexRepoHead) Resync(string) error           { return nil }
func (h codexRepoHead) Held() string                  { return "" }

func (h codexRepoHead) Summary() (string, error) {
	state, err := h.ops.CodexEnabledState()
	if err != nil {
		return "", err
	}
	if state == "" {
		state = "unknown"
	}
	running, err := h.ops.CodexActive()
	if err != nil {
		return "", err
	}
	if running {
		return h.name + "=" + state + "/running", nil
	}
	return h.name + "=" + state + "/unavailable", nil
}

// Column is empty: the Codex head has no per-repository state.
func (h codexRepoHead) Column() string { return "" }

func (h codexRepoHead) Cells(names []string) ([]string, error) {
	return make([]string, len(names)), nil
}

// CursorRepoHead is the Cursor head for the repository commands: one
// cursor-remote@<repo> instance per repository. name is the provider's
// name.
func CursorRepoHead(name string, ops CursorOps) Head { return cursorRepoHead{name, ops} }

type cursorRepoHead struct {
	name string
	ops  CursorOps
}

func (h cursorRepoHead) Label() string { return "Cursor" }
func (h cursorRepoHead) Attach() error { return h.ops.ReconcileCursor() }

func (h cursorRepoHead) Attached(name string) (bool, error) {
	enabled, active, err := h.ops.CursorUnitState(name)
	if err != nil {
		return false, fmt.Errorf("failed to query Cursor head state for %q: %w", name, err)
	}
	return enabled || active, nil
}

// Revoke is a no-op: the Cursor head holds each repository in its own
// unit, which Detach disables.
func (h cursorRepoHead) Revoke() (string, error) { return "", nil }

func (h cursorRepoHead) Detach(name string) error { return h.ops.DisableCursorUnit(name) }
func (h cursorRepoHead) Resync(string) error      { return nil }
func (h cursorRepoHead) Held() string             { return "" }

func (h cursorRepoHead) Summary() (string, error) {
	enabled, err := h.ops.CursorHeadEnabled()
	if err != nil {
		return "", err
	}
	if enabled {
		return h.name + "=enabled", nil
	}
	return h.name + "=disabled", nil
}

func (h cursorRepoHead) Column() string { return strings.ToUpper(h.name) + "_HEAD" }

func (h cursorRepoHead) Cells(names []string) ([]string, error) {
	cells := make([]string, 0, len(names))
	for _, name := range names {
		enabled, active, err := h.ops.CursorUnitState(name)
		if err != nil {
			return nil, fmt.Errorf("failed to query Cursor head state for %q: %w", name, err)
		}
		state := "-"
		switch {
		case active:
			state = "active"
		case enabled:
			state = "enabled"
		}
		cells = append(cells, state)
	}
	return cells, nil
}
