package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// Coding CLIs are installed and updated by their vendors' own installers
// and updaters; codvps keeps no copies and pins no versions.

// operatorProvisioner runs vendor installers and updaters as the invoking
// operator, streaming their output.
func operatorProvisioner(layout *paths.Layout, r runner.Runner) *providers.OperatorProvisioner {
	return &providers.OperatorProvisioner{HomeDir: layout.Home(), Runner: r, Out: os.Stdout, Diag: os.Stderr}
}

// ensureInstalled installs p's CLI on first use (`enable`, `login`).
func ensureInstalled(p *providers.Provider, layout *paths.Layout, r runner.Runner) error {
	if p.Provision == nil || p.Provision.Run == nil {
		return nil
	}
	installed, err := providers.EnsureInstalled(p, operatorProvisioner(layout, r), "/")
	if installed {
		fmt.Printf("Installed %s with its vendor installer.\n", p.Label)
	}
	return err
}

// updateTTY reports whether update may ask before restarting heads; a
// variable so tests can answer.
var updateTTY = func() bool { return isTerminal(os.Stdin) && isTerminal(os.Stdout) }

// runUpdate is `codvps update [provider] [--yes]`: each selected CLI's
// vendor updater, then a restart of the active heads whose CLI changed.
func runUpdate(args []string) error {
	yes, word := false, ""
	for _, a := range args {
		switch {
		case a == "--yes":
			yes = true
		case word == "":
			word = a
		default:
			return invalidUsage("codvps update [provider] [--yes]")
		}
	}
	layout, err := paths.New("")
	if err != nil {
		return fmt.Errorf("failed to initialize paths: %w", err)
	}
	names, err := catalog().ResolveProviders(layout, word)
	if err != nil {
		return err
	}
	r := runner.NewExecRunner()
	return update(os.Stdout, os.Stdin, layout, r, operatorProvisioner(layout, r), currentOperator(), names, yes, updateTTY())
}

// update is runUpdate with its inputs explicit.
func update(out io.Writer, in io.Reader, layout *paths.Layout, r runner.Runner, prov providers.Provisioner,
	operator string, names []string, yes, tty bool) error {
	var restart []providers.Unit
	var failures []string
	for _, name := range names {
		p := providers.All().Lookup(name)
		if p == nil || p.Provision == nil || p.Provision.Update == nil {
			continue
		}
		if p.Installed("/", layout.Home()) == "" {
			_, _ = fmt.Fprintf(out, "%s: not installed (codvps enable %s installs it)\n", name, name)
			continue
		}
		res, err := providers.Update(p, prov, r, "/")
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if !res.Changed() {
			_, _ = fmt.Fprintf(out, "%s: %s is current\n", name, res.After)
			continue
		}
		_, _ = fmt.Fprintf(out, "%s: %s -> %s\n", name, res.Before, res.After)
		restart = append(restart, providers.ActiveUnits(p, r, operator)...)
	}
	if len(restart) > 0 {
		var unitNames []string
		for _, u := range restart {
			unitNames = append(unitNames, u.Name)
		}
		switch {
		case yes:
		case !tty:
			_, _ = fmt.Fprintf(out, "Rerun with --yes to restart onto the new versions (interrupts live sessions): %s\n", strings.Join(unitNames, " "))
			restart = nil
		default:
			_, _ = fmt.Fprintf(out, "Restart %s now? Live sessions are interrupted. Type yes to continue: ", strings.Join(unitNames, " "))
			line, _ := bufio.NewReader(in).ReadString('\n')
			if strings.TrimSpace(line) != "yes" {
				_, _ = fmt.Fprintln(out, "Not restarted; the heads pick up the new versions on their next restart.")
				restart = nil
			}
		}
		for _, u := range restart {
			if err := providers.RestartUnit(r, u); err != nil {
				failures = append(failures, fmt.Sprintf("could not restart %s: %v", u.Name, err))
				continue
			}
			_, _ = fmt.Fprintf(out, "restarted %s\n", u.Name)
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

// selectedProvider is the component gate for a provider on this host. An
// unreadable registry selects nothing.
