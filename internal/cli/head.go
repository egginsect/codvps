package cli

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// headEnv is the production object graph shared by the head, repo,
// status, doctor and repo commands: every provider's head.
type headEnv struct {
	layout     *paths.Layout
	git        runner.Runner
	runner     runner.Runner
	operator   string
	providers  providers.Set
	heads      *head.Heads
	components *components.Registry
}

// headDeps are headEnv's explicit dependencies.
type headDeps struct {
	layout *paths.Layout
	// git runs git for registry pruning.
	git runner.Runner
	// system runs systemctl/journalctl/loginctl/sudo and the version
	// probes.
	system runner.Runner
	// exec builds a subprocess runner with extra environment for a head's
	// own CLI.
	exec      func(env ...string) runner.Runner
	operator  string
	sleep     func(time.Duration)
	now       func() time.Time
	providers providers.Set
}

// newHeadEnv wires headEnv for the current operator: a real subprocess
// runner for git (with GIT_TERMINAL_PROMPT=0, so a reachability check
// against an unreachable/private remote fails loudly instead of hanging on
// a credential prompt), one for systemctl/journalctl/loginctl/sudo and
// the version probes, and one per head CLI.
func newHeadEnv() (*headEnv, error) {
	layout, err := paths.New("")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize paths: %w", err)
	}
	return buildHeadEnv(headDeps{
		layout:    layout,
		git:       runner.NewExecRunner("GIT_TERMINAL_PROMPT=0"),
		system:    runner.NewExecRunner(),
		exec:      func(env ...string) runner.Runner { return runner.NewExecRunner(env...) },
		operator:  currentOperator(),
		sleep:     time.Sleep,
		now:       time.Now,
		providers: providers.All(),
	})
}

// buildHeadEnv assembles headEnv from explicit dependencies.
func buildHeadEnv(d headDeps) (*headEnv, error) {
	heads, err := providers.NewHeads(head.HeadsOptions{
		Layout:   d.layout,
		Git:      d.git,
		System:   d.system,
		Exec:     d.exec,
		Operator: d.operator,
		Out:      os.Stdout,
		Diag:     os.Stderr,
		Sleep:    d.sleep,
		Now:      d.now,
	})
	if err != nil {
		return nil, err
	}
	registry, err := (components.Catalog{Providers: d.providers}).ReadAt(d.layout)
	if err != nil {
		return nil, fmt.Errorf("failed to read component registry: %w", err)
	}
	return &headEnv{
		layout:     d.layout,
		git:        d.git,
		runner:     d.system,
		operator:   d.operator,
		providers:  d.providers,
		heads:      heads,
		components: registry,
	}, nil
}

// currentOperator names the account whose codex-remote@<operator> unit and
// linger state codvps reports. If the account cannot be resolved the unit
// name falls back to "unknown", which matches no installed unit and so is
// reported as not-found rather than acting on someone else's unit.
func currentOperator() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "unknown"
}

// headProviders are the providers with a head, in provider order.
func headProviders(set providers.Set) providers.Set {
	return set.Where(func(p *providers.Provider) bool { return p.Head != nil })
}

// headSetEnabled implements `codvps head enable|disable <provider>`.
// Enabling is gated on the provider being selected; disabling never is, so
// an operator can always switch a head off.
func (e *headEnv) headSetEnabled(name string, enable bool) error {
	p := headProviders(e.providers).Lookup(name)
	if p == nil {
		return fmt.Errorf("%s has no Remote Control head", name)
	}
	if enable {
		if err := (components.Catalog{Providers: e.providers}).RequireSelectedForHead(e.layout, name); err != nil {
			return err
		}
	}
	return p.Head.Host(e.heads).SetEnabled(enable)
}

func runHeadSetEnabled(provider string, enable bool) error {
	env, err := newHeadEnv()
	if err != nil {
		return err
	}
	// The CLI is installed on first use: enabling a head whose CLI is
	// missing runs its vendor installer first.
	if p := env.providers.Lookup(provider); enable && p != nil {
		if err := ensureInstalled(p, env.layout, env.runner); err != nil {
			return err
		}
	}
	return env.headSetEnabled(provider, enable)
}

func runHeadList() error {
	env, err := newHeadEnv()
	if err != nil {
		return err
	}
	return head.RenderList(os.Stdout, env.providers.HeadHosts(env.heads))
}

// runHeadAction runs a head's optional action (pair, ensure) for the
// provider called name.
func runHeadAction(name string, action func(*providers.Head) func(*head.Heads) error) error {
	env, err := newHeadEnv()
	if err != nil {
		return err
	}
	p := headProviders(env.providers).Lookup(name)
	if p == nil || action(p.Head) == nil {
		return fmt.Errorf("%s has no such head action", name)
	}
	return action(p.Head)(env.heads)
}

// interactiveTools are the coding CLIs whose interactive versions status
// reports.
// interactiveTools are the coding CLIs whose versions status reports. A
// CLI whose installer puts it outside PATH (ExecutablePaths) is reported
// from where it is installed under home.
func interactiveTools(set providers.Set, home string) []head.Tool {
	var out []head.Tool
	for _, p := range set.Implemented(providers.CodingCLI) {
		binary := p.Binary
		for _, rel := range p.ExecutablePaths {
			if path := filepath.Join(home, rel); isExecutableFile(path) {
				binary = path
				break
			}
		}
		out = append(out, head.Tool{Label: p.Label, Binary: binary})
	}
	return out
}

// isExecutableFile reports whether path resolves to an executable regular
// file (symlinks followed).
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0
}

func runStatus() error {
	env, err := newHeadEnv()
	if err != nil {
		return err
	}

	// Print component registry info
	clis := env.components.Selected()
	if len(clis) == 0 {
		fmt.Println("Coding CLIs: none")
	} else {
		fmt.Printf("Coding CLIs: %s\n", strings.Join(clis, ", "))
	}
	switchVal := env.components.SwitchValue()
	if switchVal == "" {
		switchVal = "none"
	}
	fmt.Printf("Switch: %s\n", switchVal)
	fmt.Println()

	return head.RenderStatus(os.Stdout, head.StatusOptions{
		Heads:       env.providers.HeadHosts(env.heads),
		Interactive: interactiveTools(env.providers, env.layout.Home()),
		Tools:       env.runner,
		Operator:    env.operator,
	})
}
