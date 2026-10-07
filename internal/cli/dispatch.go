package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/egginsect/codvps/internal/providers"
)

var commands = map[string]Command{
	"login":      &loginCmd{},
	"repo":       &repoCmd{},
	"enable":     &enableCmd{},
	"disable":    &disableCmd{},
	"list":       &listCmd{},
	"pair":       &pairCmd{},
	"ensure":     &ensureCmd{},
	"head":       &headCmd{},
	"update":     &updateCmd{},
	"config":     &configCmd{},
	"components": &componentsCmd{},
	"status":     &statusCmd{},
	"doctor":     &doctorCmd{},
	"install":    &installCmd{},
	"uninstall":  &uninstallCmd{},
	"restart":    &restartCmd{},
	"internal":   &internalCmd{},
	"help":       &helpCmd{},
	"version":    &versionCmd{},
}

type Command interface {
	Execute(args []string) error
}

// Execute runs the dispatcher
func Execute(args []string) error {
	if len(args) == 0 {
		printHelp()
		return nil
	}

	cmdName := args[0]
	if cmd, exists := commands[cmdName]; exists {
		return cmd.Execute(args[1:])
	}

	fmt.Fprintf(os.Stderr, "codvps: unknown command: %s\n", cmdName)
	return &unknownCommandError{name: cmdName}
}

// helpEntry is one line of the top-level help: a full command form and its summary.
type helpEntry struct {
	form    string
	summary string
}

// helpNameWidth is the column where summaries start; longer forms wrap the
// summary onto an indented next line.
const helpNameWidth = 28

// helpEntries is the top-level help: the provider rows (login, enable, disable, pair) come
// from the provider definitions, so a new coding CLI shows up here too.
func helpEntries() []helpEntry {
	set := providers.All()
	var entries []helpEntry
	for _, p := range loginProviders(set) {
		entries = append(entries, helpEntry{"login " + p.Name, p.Login.Summary})
	}
	heads := "<" + strings.Join(headProviders(set).Names(), "|") + ">"
	entries = append(entries,
		helpEntry{"login github", "Authenticate GitHub, then set up VPS SSH access and Git identity"},
		helpEntry{"repo add <url>", "Clone or reattach a repository and attach it to every enabled head"},
		helpEntry{"repo list", "List registered repositories and each head's state"},
		helpEntry{"repo remove <name>", "Deregister a repository and detach it from every head; keep the checkout"},
		helpEntry{"enable " + heads, "Enable a host head and attach every registered repository"},
		helpEntry{"disable " + heads, "Disable a host head and detach every repository from it"},
		helpEntry{"list", "Show host heads and per-repository heads"},
	)
	for _, p := range headProviders(set) {
		if p.Head.Pair != nil {
			entries = append(entries, helpEntry{"pair " + p.Name, p.Head.PairSummary})
		}
	}
	entries = append(entries, helpEntry{"update [provider] [--yes]", "Update the coding CLIs with their vendors' updaters (default: all)"})
	entries = append(entries, helpConfigEntries...)
	return append(entries, helpEntriesAfterConfig...)
}

var helpConfigEntries = []helpEntry{
	{"config link <repo-url>", "Keep your agent config (CLAUDE.md, skills, …) in a git repo: clone it and link it into place"},
}

var helpEntriesAfterConfig = []helpEntry{
	{"status", "Show host and per-repository status"},
	{"doctor", "Check codvps prerequisites and health"},
	{"install [--components <list>] [--switch <name>] [--skip-provision]", "Install or repair codvps on this host (run with sudo)"},
	{"uninstall [--dry-run]", "Remove what codvps installed; keep repositories, state and credentials (run with sudo)"},
	{"help", "Show this help"},
	{"version", "Show version, commit and build date"},
}

// helpText renders the top-level help deterministically.
func helpText() string {
	var b strings.Builder
	b.WriteString("Usage: codvps <command>\n\nCommands:\n")
	indent := strings.Repeat(" ", 2+helpNameWidth+1)
	for _, e := range helpEntries() {
		if len(e.form) > helpNameWidth {
			fmt.Fprintf(&b, "  %s\n%s%s\n", e.form, indent, e.summary)
			continue
		}
		fmt.Fprintf(&b, "  %-*s %s\n", helpNameWidth, e.form, e.summary)
	}
	return b.String()
}

func printHelp() {
	fmt.Print(helpText())
}

// InvalidUsageError is returned when a command is called with invalid arguments
type InvalidUsageError struct {
	message string
}

func (e *InvalidUsageError) Error() string {
	return e.message
}

// Reported reports that the usage message has already been printed to
// stderr, so ReportError must not print this error again.
func (e *InvalidUsageError) Reported() bool {
	return true
}

// NotImplementedError is returned by commands that are not yet implemented
type NotImplementedError struct {
	message string
}

func (e *NotImplementedError) Error() string {
	return e.message
}

// ExitError carries an exit code along with an error message
type ExitError struct {
	Code    int
	Message string
}

func (e *ExitError) Error() string {
	return e.Message
}

// ExitCode returns the process exit code the command asked for.
func (e *ExitError) ExitCode() int {
	return e.Code
}

// Reported reports that the not-implemented message has already been
// printed to stderr, so ReportError must not print this error again.
func (e *NotImplementedError) Reported() bool {
	return true
}

// unknownCommandError is returned when Execute is given a command name that
// has no registered handler. The message has already been printed to
// stderr by Execute, so ReportError must not print it again.
type unknownCommandError struct {
	name string
}

func (e *unknownCommandError) Error() string {
	return fmt.Sprintf("unknown command %q", e.name)
}

// Reported reports that Execute already printed this error to stderr.
func (e *unknownCommandError) Reported() bool {
	return true
}

// invalidUsage prints a usage message and returns an error
func invalidUsage(usageMsg string) error {
	fmt.Fprintf(os.Stderr, "usage: %s\n", usageMsg)
	return &InvalidUsageError{message: "invalid usage"}
}
