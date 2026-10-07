package head

import (
	"errors"
	"io"
	"time"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/runner"
)

// Heads are the host heads one command drives. They are built together
// because they share dependencies. Each
// coding CLI's definition in internal/providers picks its own head out of
// this set.
type Heads struct {
	Claude   *Claude
	Codex    *Codex
	Cursor   *Cursor
	OpenCode *OpenCode
}

// HeadsOptions are the heads' shared dependencies.
type HeadsOptions struct {
	Layout *paths.Layout
	// Git runs git for registry pruning.
	Git runner.Runner
	// System runs systemctl/journalctl/loginctl/sudo and the version
	// probes.
	System runner.Runner
	// Exec builds a runner whose children get env on top of codvps's own
	// environment.
	Exec func(env ...string) runner.Runner
	// CodexBinary is the Codex CLI's executable name.
	CodexBinary string
	Operator    string
	Out         io.Writer
	Diag        io.Writer
	Sleep       func(time.Duration)
	Now         func() time.Time
}

// NewHeads builds every head from o; each head refuses a missing
// dependency it needs.
func NewHeads(o HeadsOptions) (*Heads, error) {
	if o.Exec == nil || o.Layout == nil {
		return nil, errors.New("heads: Exec and Layout are required")
	}
	claude, err := NewClaude(Options{Layout: o.Layout, Git: o.Git, Systemd: o.System, Diag: o.Diag, Sleep: o.Sleep})
	if err != nil {
		return nil, err
	}
	codex, err := NewCodex(CodexOptions{
		Layout:    o.Layout,
		System:    o.System,
		Codex:     o.Exec(),
		Binary:    o.CodexBinary,
		Operator:  o.Operator,
		Listening: ControlSocketListening,
		Out:       o.Out,
		Diag:      o.Diag,
		Sleep:     o.Sleep,
		Now:       o.Now,
	})
	if err != nil {
		return nil, err
	}
	cursor, err := NewCursor(CursorOptions{Layout: o.Layout, Git: o.Git, Systemd: o.System, Diag: o.Diag, Sleep: o.Sleep})
	if err != nil {
		return nil, err
	}
	opencode, err := NewOpenCode(OpenCodeOptions{Layout: o.Layout, Systemd: o.System, Diag: o.Diag, Sleep: o.Sleep})
	if err != nil {
		return nil, err
	}
	return &Heads{Claude: claude, Codex: codex, Cursor: cursor, OpenCode: opencode}, nil
}
