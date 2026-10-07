package providers

import (
	"errors"

	"github.com/egginsect/codvps/internal/doctor"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// Login is `codvps login <name>`: the CLI's own login command, gated on
// the provider being selected.
type Login struct {
	// Summary is the help line ("Authenticate Claude Code").
	Summary string
	// Command is the login command and what must follow it.
	Command login.CLI
}

// Head is the provider's host-scoped Remote Control head: `codvps head
// enable|disable|list` and the head rows of `codvps status`.
type Head struct {
	// Host picks the provider's head out of the heads built for a command.
	Host func(h *head.Heads) head.Host
	// Pair, when set, is `codvps head pair <name>` and PairSummary its help
	// line.
	Pair        func(h *head.Heads) error
	PairSummary string
	// Ensure, when set, is the hidden `codvps head ensure <name>` a
	// watchdog unit runs.
	Ensure func(h *head.Heads) error
	// Repo is the head as `codvps repo add|list|remove` drive it: attach
	// on add, revoke/detach/resync on remove, and its repo list column. r
	// runs the head's systemctl calls.
	Repo func(h *head.Heads, layout *paths.Layout, r runner.Runner) repo.Head
	// RepoSelectedOnly limits Repo to hosts where the provider is a
	// selected component, so a head added after a host was installed does
	// not change repo add/list/remove there. Heads that predate component
	// selection leave it false and are always driven.
	RepoSelectedOnly bool
}

// Doctor builds a provider's doctor checks from the heads built for the
// run (a check may probe the provider's own head).
type Doctor func(h *head.Heads) doctor.Suite

// Internal is a `codvps internal <name> ...` helper a provider's own unit
// files run (not an operator command).
type Internal struct {
	// Usage is shown for invalid arguments.
	Usage string
	// Run runs the helper with the arguments after its name. It returns
	// ErrUsage for invalid arguments, and an error with an ExitCode() int
	// method when the process must exit with a specific status.
	Run func(args []string) error
}

// ErrUsage marks an invalid internal-helper invocation.
var ErrUsage = errors.New("invalid usage")

// ExitError carries the exit status an internal helper's process must exit
// with, as a unit's ExecStart needs it.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }

// Unwrap returns the underlying error.
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode is the process exit status.
func (e *ExitError) ExitCode() int { return e.Code }

// NewHeads builds the heads of the defined providers from their shared
// dependencies (the head implementations take the CLI binaries these
// definitions declare).
func NewHeads(o head.HeadsOptions) (*head.Heads, error) {
	o.CodexBinary = codexBinary
	return head.NewHeads(o)
}

// HeadHosts are the heads of the providers in s that have one, named by
// provider, in provider order.
func (s Set) HeadHosts(h *head.Heads) []head.Named {
	var out []head.Named
	for _, p := range s {
		if p.Head != nil {
			out = append(out, head.Named{Name: p.Name, Host: p.Head.Host(h)})
		}
	}
	return out
}

