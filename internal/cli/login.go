package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

// handleLoginProvider handles `codvps login <provider>`.
func handleLoginProvider(p *providers.Provider, args []string) error {
	if len(args) > 0 {
		return invalidUsage("codvps login " + p.Name)
	}
	layout, err := paths.New("")
	if err != nil {
		return fmt.Errorf("failed to initialize paths: %w", err)
	}
	if err := (components.Catalog{Providers: providers.All()}).RequireSelected(layout, p.Name); err != nil {
		return err
	}
	// The CLI is installed on first use; its login needs it.
	if err := ensureInstalled(p, layout, runner.NewExecRunner()); err != nil {
		return err
	}
	return runLogin(layout, providers.All(), p.Name, login.NewContext())
}

// runLogin gates `login <name>` on the provider being selected, then runs
// the provider's own login command.
func runLogin(layout *paths.Layout, set providers.Set, name string, ctx *login.Context) error {
	p := loginProviders(set).Lookup(name)
	if p == nil {
		return fmt.Errorf("%s has no login", name)
	}
	if err := (components.Catalog{Providers: set}).RequireSelected(layout, name); err != nil {
		return err
	}
	if err := login.Run(ctx, p.Login.Command); err != nil {
		return &ExitError{Code: 1, Message: err.Error()}
	}
	return nil
}

const loginGitHubUsage = `Usage: codvps login github [flags]

No terminal is needed: when not logged in, GitHub's device flow starts
anyway and prints its URL and one-time code (open it on any device), and each
prompt without a terminal takes its default answer, reported as "assuming yes:
...". A missing or too-old gh is installed or upgraded first (needs sudo; without
a terminal, run sudo codvps install instead).

Flags:
  --non-interactive         Never prompt and never start the GitHub login: a step
                             that would otherwise prompt reports pending instead
                             (auth exits 2 immediately if not already logged in)
  --key <choice>             skip | generate | existing:<path>
  --passphrase-empty         Generate the key without a passphrase; required with
                             --key generate (or the default key) outside a tty
  --identity-name <name>     Set git user.name if it is not already set
  --identity-email <email>   Set git user.email if it is not already set
  --yes                      Confirm SSH key registration without a terminal, and
                             answer every prompt with its default even on one
  --defer                    Preflight only; make no changes; exit 0
  --help                     Show this help
`

// handleLoginGitHub handles 'login github' with its full flag set.
func handleLoginGitHub(args []string) error {
	fs := flag.NewFlagSet("login github", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stdout, loginGitHubUsage)
	}

	nonInteractive := fs.Bool("non-interactive", false, "run non-interactively")
	keyChoice := fs.String("key", "", "skip | generate | existing:<path>")
	passphraseEmpty := fs.Bool("passphrase-empty", false, "acknowledge an empty passphrase for non-interactive generation")
	identityName := fs.String("identity-name", "", "git user.name")
	identityEmail := fs.String("identity-email", "", "git user.email")
	yes := fs.Bool("yes", false, "confirm SSH key registration non-interactively")
	deferOnly := fs.Bool("defer", false, "preflight only")
	help := fs.Bool("help", false, "show help")

	if err := fs.Parse(args); err != nil {
		// The flag package has already printed the parse error and usage.
		return &reportedError{err: &ExitError{Code: 2, Message: err.Error()}}
	}
	if *help {
		fs.Usage()
		return nil
	}
	if fs.NArg() > 0 {
		return invalidUsage("codvps login github [flags]")
	}

	ctx := login.NewContext()
	code, err := login.GitHub(ctx, *nonInteractive, *keyChoice, *passphraseEmpty, *identityName, *identityEmail, *yes, *deferOnly)
	if code != 0 {
		msg := "github setup incomplete"
		if err != nil {
			msg = err.Error()
		}
		return &ExitError{Code: code, Message: msg}
	}
	return nil
}
