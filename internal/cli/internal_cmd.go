package cli

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/shellenv"
)

// internalCmd handles 'internal' command: codvps's own helpers, plus the
// helpers each provider's unit files run (providers.Internal).
type internalCmd struct{}

// coreInternal are the helpers every host has, in usage order.
var coreInternal = []string{"operator-state", "session-check", "shell-exec"}

func (c *internalCmd) Execute(args []string) error {
	helpers := providerInternals(providers.All())
	names := append([]string(nil), coreInternal...)
	for name := range helpers {
		names = append(names, name)
	}
	sort.Strings(names)
	usage := "codvps internal <" + strings.Join(names, "|") + ">"
	if len(args) == 0 {
		return invalidUsage(usage)
	}

	sub := args[0]
	switch sub {
	case "operator-state":
		return internalOperatorState(args[1:])
	case "shell-exec":
		return internalShellExec(args[1:], providers.All(), shellenv.Default())
	case "session-check":
		// The retired runtime updater's Codex SessionStart hook: a no-op so
		// a registration install has not removed yet never fails a session.
		return nil
	}
	helper, ok := helpers[sub]
	if !ok {
		return invalidUsage(usage)
	}
	if err := helper.Run(args[1:]); err != nil {
		if errors.Is(err, providers.ErrUsage) {
			return invalidUsage(helper.Usage)
		}
		return err
	}
	return nil
}

// providerInternals are every provider's internal helpers, by name.
func providerInternals(set providers.Set) map[string]providers.Internal {
	out := map[string]providers.Internal{}
	for _, p := range set {
		for name, h := range p.Internal {
			out[name] = h
		}
	}
	return out
}

// namespaceFromEnv resolves the codvps namespace directly from the
// environment, without going through paths.New: the root helpers run under
// systemd or sudo, where HOME may not be set at all for root, and every
// path they touch belongs to the operator's home, never the caller's own.

const shellExecUsage = "codvps internal shell-exec [--provider NAME] [--unset VAR]... -- CMD [ARGS...]"

// internalShellExec is `codvps internal shell-exec`: replace this process
// with CMD, started in the operator's login-shell environment minus the
// named provider's Env.Remove and every --unset variable. It returns only
// when CMD could not be started.
func internalShellExec(args []string, set providers.Set, shell shellenv.Options) error {
	var provider string
	var remove []string
	i := 0
parse:
	for ; i < len(args); i++ {
		switch args[i] {
		case "--":
			break parse
		case "--provider", "--unset":
			if i+1 >= len(args) {
				return invalidUsage(shellExecUsage)
			}
			if args[i] == "--provider" {
				provider = args[i+1]
			} else {
				remove = append(remove, args[i+1])
			}
			i++
		default:
			return invalidUsage(shellExecUsage)
		}
	}
	argv := []string(nil)
	if i < len(args) {
		argv = args[i+1:]
	}
	if len(argv) == 0 {
		return invalidUsage(shellExecUsage)
	}
	if provider != "" {
		p := set.Lookup(provider)
		if p == nil {
			return fmt.Errorf("shell-exec: unknown provider %q (known: %s)", provider, strings.Join(set.Names(), ", "))
		}
		if p.Env != nil {
			remove = append(remove, p.Env.Remove...)
		}
	}
	return shell.Run(remove, argv)
}
