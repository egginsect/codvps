package cli

import (
	"fmt"
	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/providers"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egginsect/codvps/internal/buildinfo"
	"github.com/egginsect/codvps/internal/configlink"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
)

// loginCmd handles 'login' command
type loginCmd struct{}

func (c *loginCmd) Execute(args []string) error {
	logins := loginProviders(providers.All())
	usage := "codvps login <" + strings.Join(append(logins.Names(), "github"), "|") + "> [--non-interactive]"
	if len(args) == 0 {
		return invalidUsage(usage)
	}
	if args[0] == "github" {
		// github accepts various flags
		return handleLoginGitHub(args[1:])
	}
	p := logins.Lookup(args[0])
	if p == nil {
		return invalidUsage(usage)
	}
	return handleLoginProvider(p, args[1:])
}

// loginProviders are the providers with a `codvps login <name>`.
func loginProviders(set providers.Set) providers.Set {
	return set.Where(func(p *providers.Provider) bool { return p.Login != nil })
}

// repoCmd handles 'repo' command
type repoCmd struct{}

func (c *repoCmd) Execute(args []string) error {
	if len(args) == 0 {
		return invalidUsage("codvps repo <add|list|remove>")
	}

	sub := args[0]
	switch sub {
	case "help", "--help":
		if len(args) != 1 {
			return invalidUsage("codvps repo help")
		}
		fmt.Print(repoUsage)
		return nil
	case "add":
		for _, a := range args[1:] {
			if a == "--head" || a == "--trusted" {
				return fmt.Errorf("repo add no longer accepts --head or --trusted; heads are host-scoped, so use codvps enable <claude|codex> separately")
			}
		}
		if len(args) != 2 {
			return invalidUsage("codvps repo add <url>")
		}
		if strings.HasPrefix(args[1], "-") {
			return fmt.Errorf("unknown repo add option: %s", args[1])
		}
		return repoAdd(args[1])
	case "list":
		if len(args) != 1 {
			return invalidUsage("codvps repo list")
		}
		return repoList()
	case "remove":
		if len(args) != 2 {
			return invalidUsage("codvps repo remove <name>")
		}
		return repoRemove(args[1])
	default:
		return fmt.Errorf("unknown repo command: %s", sub)
	}
}

// repoUsage is `codvps repo help`: the reference's repo_usage, naming
// codvps.
const repoUsage = `Usage: codvps repo <command>

Repository commands:
  add <url>          Clone or safely reattach a repository and attach it to every enabled head
  list               List registered repositories and their Claude head state
  remove <name>      Deregister a repository and detach it from every head; keep the checkout

Heads are host-scoped, not per repository: codvps enable|disable <claude|codex|cursor>.
Every registered repository is attached to every enabled head.
`

// enableCmd handles 'enable' command (top-level, was 'head enable')
type enableCmd struct{}

func (c *enableCmd) Execute(args []string) error {
	return enableSetEnabledCmd(args)
}

// disableCmd handles 'disable' command (top-level, was 'head disable')
type disableCmd struct{}

func (c *disableCmd) Execute(args []string) error {
	return disableSetEnabledCmd(args)
}

// listCmd handles 'list' command (top-level, was 'head list')
type listCmd struct{}

func (c *listCmd) Execute(args []string) error {
	if len(args) != 0 {
		return invalidUsage("codvps list")
	}
	return runHeadList()
}

// pairCmd handles 'pair' command (top-level, was 'head pair')
type pairCmd struct{}

func (c *pairCmd) Execute(args []string) error {
	return pairActionCmd(args)
}

// ensureCmd handles 'ensure' command (top-level, hidden, was 'head ensure')
type ensureCmd struct{}

func (c *ensureCmd) Execute(args []string) error {
	return ensureActionCmd(args)
}

// headCmd handles 'head' command (now hidden, delegates to top-level forms)
type headCmd struct{}

func (c *headCmd) Execute(args []string) error {
	if len(args) == 0 {
		return invalidUsage("codvps head <enable|disable|list|pair|ensure>")
	}

	sub := args[0]
	switch sub {
	case "enable", "disable":
		return headSetEnabledCmd(sub, args[1:])
	case "list":
		if len(args) != 1 {
			return invalidUsage("codvps list")
		}
		return runHeadList()
	case "pair":
		return headActionCmd(sub, args[1:], func(h *providers.Head) func(*head.Heads) error { return h.Pair })
	case "ensure":
		// Hidden: the watchdog's.
		return headActionCmd(sub, args[1:], func(h *providers.Head) func(*head.Heads) error { return h.Ensure })
	default:
		return fmt.Errorf("unknown head command: %s", sub)
	}
}

// headActionCmd validates `head pair|ensure <provider>` against the
// providers whose head has that action.
func headActionCmd(verb string, args []string, action func(*providers.Head) func(*head.Heads) error) error {
	var names []string
	for _, p := range headProviders(providers.All()) {
		if action(p.Head) != nil {
			names = append(names, p.Name)
		}
	}
	if len(args) != 1 || !contains(names, args[0]) {
		return invalidUsage("codvps head " + verb + " " + strings.Join(names, "|"))
	}
	return runHeadAction(args[0], action)
}

// enableSetEnabledCmd validates `enable <provider>` (top-level form)
func enableSetEnabledCmd(args []string) error {
	names := headProviders(providers.All()).Names()
	if len(args) == 0 || !contains(names, args[0]) {
		return invalidUsage("codvps enable <" + strings.Join(names, "|") + ">")
	}
	provider := args[0]
	if len(args) != 1 {
		return invalidUsage("codvps enable " + provider)
	}
	return runHeadSetEnabled(provider, true)
}

// disableSetEnabledCmd validates `disable <provider>` (top-level form)
func disableSetEnabledCmd(args []string) error {
	names := headProviders(providers.All()).Names()
	if len(args) == 0 || !contains(names, args[0]) {
		return invalidUsage("codvps disable <" + strings.Join(names, "|") + ">")
	}
	provider := args[0]
	if len(args) != 1 {
		return invalidUsage("codvps disable " + provider)
	}
	return runHeadSetEnabled(provider, false)
}

// pairActionCmd validates `pair <provider>` (top-level form)
func pairActionCmd(args []string) error {
	return pairActionCmdWithVerb("pair", args)
}

// ensureActionCmd validates `ensure <provider>` (top-level form, hidden)
func ensureActionCmd(args []string) error {
	return ensureActionCmdWithVerb("ensure", args)
}

// pairActionCmdWithVerb is shared by both `pair` (top-level) and `head pair`
func pairActionCmdWithVerb(verb string, args []string) error {
	var names []string
	for _, p := range headProviders(providers.All()) {
		if p.Head.Pair != nil {
			names = append(names, p.Name)
		}
	}
	if len(args) != 1 || !contains(names, args[0]) {
		return invalidUsage("codvps " + verb + " " + strings.Join(names, "|"))
	}
	return runHeadAction(args[0], func(h *providers.Head) func(*head.Heads) error { return h.Pair })
}

// ensureActionCmdWithVerb is shared by both `ensure` (top-level) and `head ensure`
func ensureActionCmdWithVerb(verb string, args []string) error {
	var names []string
	for _, p := range headProviders(providers.All()) {
		if p.Head.Ensure != nil {
			names = append(names, p.Name)
		}
	}
	if len(args) != 1 || !contains(names, args[0]) {
		return invalidUsage("codvps " + verb + " " + strings.Join(names, "|"))
	}
	return runHeadAction(args[0], func(h *providers.Head) func(*head.Heads) error { return h.Ensure })
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// headSetEnabledCmd validates `head enable|disable <provider>`. A head
// names no repository, so any extra word is refused with the per-provider
// usage line (the reference's migration guidance for the removed
// per-repository spellings).
func headSetEnabledCmd(action string, args []string) error {
	names := headProviders(providers.All()).Names()
	if len(args) == 0 || !contains(names, args[0]) {
		return invalidUsage("codvps head " + action + " <" + strings.Join(names, "|") + ">")
	}
	provider := args[0]
	if len(args) != 1 {
		return invalidUsage("codvps head " + action + " " + provider)
	}
	return runHeadSetEnabled(provider, action == "enable")
}

// removedCommandError is a removed command spelling: it exits 1 with the
// migration guidance the reference gives for it.
func removedCommandError(guidance string) error {
	return fmt.Errorf("removed: %s", guidance)
}

// updateUsage is `codvps update help`.
const updateUsage = `Usage: codvps update [provider] [--yes]

Runs each installed coding CLI's own vendor updater (default: every
selected one) and prints its version before and after. Heads whose CLI
changed are restarted with --yes, or after you type yes at the prompt;
restarting interrupts live sessions.
`

// updateCmd handles 'update' command
type updateCmd struct{}

func (c *updateCmd) Execute(args []string) error {
	// update accepts optional [provider] [--yes]
	if len(args) > 0 {
		switch args[0] {
		case "check", "stage", "status", "apply":
			return removedCommandError("use codvps update [provider]")
		case "help", "--help":
			if len(args) == 1 {
				fmt.Print(updateUsage)
				return nil
			}
		}
	}
	if len(args) > 2 {
		return invalidUsage("codvps update [provider] [--yes]")
	}
	return runUpdate(args)
}

// configCmd handles 'config' command
type configCmd struct{}

func (c *configCmd) Execute(args []string) error {
	switch {
	case len(args) == 2 && args[0] == "link":
		return runConfigLink(args[1])
	case len(args) == 1 && args[0] == "unlink":
		// Hidden: put regular copies back and forget the config repo.
		return runConfigUnlink()
	}
	return invalidUsage("codvps config link <repo-url>")
}

// runConfigLink registers the config repo like `repo add` (an already
// registered name works too), then links its mirrored files into home.
func runConfigLink(arg string) error {
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	name := arg
	if registry.ValidateName(arg) != nil {
		if name, err = registry.RepoNameFromURL(arg); err != nil {
			return fmt.Errorf("invalid repository URL: %w", err)
		}
		manager, err := newRepoManager()
		if err != nil {
			return err
		}
		if err := manager.Add(arg); err != nil {
			return err
		}
	}
	names, err := registry.Read(layout.RepositoriesPath())
	if err != nil {
		return err
	}
	checkout := filepath.Join(layout.Home(), name)
	if fi, err := os.Stat(checkout); err != nil || !fi.IsDir() || !slices.Contains(names, name) {
		return fmt.Errorf("%s is not a registered repository; run codvps config link <repo-url>", name)
	}
	res, err := configlink.Link(layout.Home(), layout.ConfigDir(), checkout)
	if err != nil {
		return err
	}
	home := layout.Home()
	short := func(p string) string {
		if rel, err := filepath.Rel(home, p); err == nil {
			return "~/" + rel
		}
		return p
	}
	fmt.Printf("Linked config repo %s: %d linked, %d already linked, %d backed up, %d refused.\n",
		short(checkout), len(res.Linked), len(res.AlreadyLinked), len(res.BackedUp), len(res.Refused))
	for target, backup := range res.BackedUp {
		fmt.Printf("  kept the repo version of %s; your previous copy is in %s\n", short(target), short(backup))
	}
	for _, r := range res.Refused {
		fmt.Printf("  refused %s: %s\n", short(r.Path), r.Reason)
	}
	fmt.Printf("Update it with git in %s; editing a linked file under ~ edits the repo.\n", short(checkout))
	if len(res.Refused) > 0 {
		return fmt.Errorf("%d path(s) were refused", len(res.Refused))
	}
	return nil
}

func runConfigUnlink() error {
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	n, err := configlink.Unlink(layout.Home(), layout.ConfigDir())
	if err != nil {
		return err
	}
	fmt.Printf("Replaced %d link(s) with regular copies; no config repo is linked.\n", n)
	return nil
}

// componentsCmd handles 'components' command
type componentsCmd struct{}

func (c *componentsCmd) Execute(args []string) error {
	if len(args) == 0 {
		return invalidUsage("codvps components <list|status>")
	}

	sub := args[0]
	switch sub {
	case "list":
		if len(args) != 1 {
			return invalidUsage("codvps components list")
		}
		return CmdComponentsList()
	case "status":
		if len(args) != 1 {
			return invalidUsage("codvps components status")
		}
		return CmdComponentsStatus()
	case "help", "--help", "-h":
		if len(args) != 1 {
			return invalidUsage("codvps components help")
		}
		return CmdComponentsUsage()
	default:
		return invalidUsage("codvps components <list|status>")
	}
}

// statusCmd handles 'status' command
type statusCmd struct{}

func (c *statusCmd) Execute(args []string) error {
	if len(args) != 0 {
		return invalidUsage("codvps status")
	}
	return runStatus()
}

// doctorCmd handles 'doctor' command
type doctorCmd struct{}

func (c *doctorCmd) Execute(args []string) error {
	if len(args) != 0 {
		return invalidUsage("codvps doctor")
	}
	return runDoctor()
}

// restartCmd is the removed standalone `restart` verb; it only points at
// its replacement.
type restartCmd struct{}

func (c *restartCmd) Execute(args []string) error {
	return removedCommandError("restart a head with systemctl; codvps update --yes restarts the heads whose CLI changed")
}

// versionCmd handles 'version' command
type versionCmd struct{}

func (c *versionCmd) Execute(args []string) error {
	if len(args) != 0 {
		return invalidUsage("codvps version")
	}
	fmt.Printf("codvps version %s (commit %s, built %s)\n",
		buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)
	return nil
}

// helpCmd handles 'help' command
type helpCmd struct{}

func (c *helpCmd) Execute(args []string) error {
	if len(args) != 0 {
		return invalidUsage("codvps help")
	}
	printHelp()
	return nil
}
