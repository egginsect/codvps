package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/head"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/session"
	"github.com/egginsect/codvps/internal/settings"
)

const runUsage = "codvps run <claude|codex|cursor> <repo> [-- CLI-ARGS...]"
const remoteUsage = "codvps remote start <claude|cursor> [<repo>] [--background|-d] | remote stop <claude|cursor> [<repo>] | remote status [<claude|cursor> [<repo>]] | remote run <claude|cursor> <repo> | remote <start|stop|pair|status> codex"

type runCmd struct{}

func (*runCmd) Execute(args []string) error {
	if len(args) < 2 || (len(args) > 2 && args[2] != "--") {
		return invalidUsage(runUsage)
	}
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	var extra []string
	if len(args) > 2 {
		extra = args[3:]
	}
	git, err := sessionGit(layout)
	if err != nil {
		return err
	}
	plan, err := session.Interactive(layout, args[0], args[1], extra, git)
	if err != nil {
		return err
	}
	return session.Execute(plan)
}

type remoteCmd struct{}

// remoteWorkers builds the background worker manager; tests replace it.
var remoteWorkers = newWorkers

func (*remoteCmd) Execute(args []string) error {
	if len(args) == 0 {
		return invalidUsage(remoteUsage)
	}
	action := args[0]
	background := false
	var positional []string
	for _, arg := range args[1:] {
		switch {
		case arg == "--background" || arg == "-d":
			background = true
		case strings.HasPrefix(arg, "-"):
			return invalidUsage(remoteUsage)
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) > 2 || (background && action != "start") || (len(positional) == 0 && action != "status") {
		return invalidUsage(remoteUsage)
	}
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	if err := requireManualMode(layout); err != nil {
		return err
	}
	provider, repository := "", ""
	if len(positional) > 0 {
		provider = positional[0]
	}
	if len(positional) > 1 {
		repository = positional[1]
	}
	switch provider {
	case "", "claude", "cursor":
	case "codex":
		return remoteCodex(layout, action, repository, background)
	default:
		return fmt.Errorf("unknown remote provider %q (choose claude, cursor or codex)", provider)
	}
	w := remoteWorkers(layout)
	switch action {
	case "status":
		return w.status(provider, repository)
	case "stop":
		return w.stop(provider, repository)
	case "start":
		if repository == "" {
			cursorNote(provider)
			if !background {
				fmt.Fprintf(os.Stderr, "No repository given: starting one background %s worker per registered repository (logs under %s).\n", provider, layout.RemoteDir())
			}
			return w.startAll(provider)
		}
		if background {
			cursorNote(provider)
			return w.startOne(provider, repository)
		}
		// One repository in the foreground is run.
		action = "run"
	}
	git, err := sessionGit(layout)
	if err != nil {
		return err
	}
	plan, err := session.Remote(layout, action, provider, repository, git)
	if err != nil {
		return err
	}
	cursorNote(provider)
	return session.Execute(plan)
}

func cursorNote(provider string) {
	if provider == "cursor" {
		fmt.Fprintln(os.Stderr, "Cursor Remote requires a Cloud Agents plan. Code and context are sent to Cursor; the worker has this account's filesystem access, beyond the selected checkout.")
	}
}

// remoteCodex is the host-wide Codex lifecycle: Codex's own daemon
// detaches, so --background changes nothing.
func remoteCodex(layout *paths.Layout, action, repository string, background bool) error {
	if repository != "" {
		return fmt.Errorf("codex Remote is host-wide; omit the repository and select its directory in the connected client")
	}
	if action == "status" {
		socket := head.ControlSocket(filepath.Join(layout.Home(), ".codex"))
		if err := head.ControlSocketListening(socket); err != nil {
			return fmt.Errorf("codex daemon is not reachable in this process namespace: %w", err)
		}
		fmt.Println("Codex control socket is listening. Client registration and UI task visibility must be verified in the connected client.")
		return nil
	}
	git, err := sessionGit(layout)
	if err != nil {
		return err
	}
	plan, err := session.Remote(layout, action, "codex", "", git)
	if err != nil {
		return err
	}
	if background {
		fmt.Fprintln(os.Stderr, "Codex Remote's daemon detaches on its own; --background is ignored.")
	}
	if action == "start" {
		fmt.Fprintln(os.Stderr, "Codex Remote is host-wide. Pair/register this host in the connected client to create visible remote tasks; a running daemon alone does not establish client access.")
	}
	return session.Execute(plan)
}

func requireManualMode(layout *paths.Layout) error {
	cfg, err := settings.Read(layout)
	if err != nil {
		return err
	}
	if cfg.SessionMode != "manual" {
		return fmt.Errorf("native user-space remote commands require: codvps config set session_mode manual (first disable any existing systemd heads; this setting does not stop them)")
	}
	return nil
}

func requireSystemdMode() error {
	layout, err := paths.New("")
	if err != nil {
		return err
	}
	cfg, err := settings.Read(layout)
	if err != nil {
		return err
	}
	if cfg.SessionMode == "manual" {
		return fmt.Errorf("session_mode is manual; start native remote workers with codvps remote start <claude|cursor|codex> [repo] [--background], or set session_mode systemd to manage installed services")
	}
	return nil
}

func manualStatus() (bool, error) {
	layout, err := paths.New("")
	if err != nil {
		return false, err
	}
	cfg, err := settings.Read(layout)
	if err != nil {
		return false, err
	}
	if cfg.SessionMode != "manual" {
		return false, nil
	}
	fmt.Println("Session mode: manual (native CLI processes; no systemd supervision)")
	for _, provider := range []string{"claude", "codex", "cursor"} {
		path, err := session.Binary(layout, provider)
		if err != nil {
			fmt.Printf("%s: not installed\n", provider)
		} else {
			fmt.Printf("%s: %s\n", provider, path)
		}
	}
	fmt.Println("Remote login, pairing and client task visibility are not inferred from CLI availability.")
	return true, repoList()
}

// manualDoctor verifies local prerequisites only. It deliberately does not
// infer authentication, pairing, or GUI visibility from an executable file.
func manualDoctor() (bool, error) {
	layout, err := paths.New("")
	if err != nil {
		return false, err
	}
	cfg, err := settings.Read(layout)
	if err != nil {
		return false, err
	}
	if cfg.SessionMode != "manual" {
		return false, nil
	}
	failures := 0
	fmt.Println("Manual mode: no systemd services or privileged provisioning required.")
	for _, name := range []string{"claude", "codex", "cursor"} {
		path, err := session.Binary(layout, name)
		if err != nil {
			fmt.Printf("MISSING %s CLI\n", name)
			failures++
		} else {
			fmt.Printf("OK %s CLI: %s\n", name, path)
		}
	}
	if _, _, code, err := runner.NewExecRunner().Run("git", "--version"); code != 0 || err != nil {
		fmt.Println("MISSING git")
		failures++
	}
	if err := repoList(); err != nil {
		return true, err
	}
	fmt.Println("Native login and remote pairing must be completed separately. Verify a visible, steerable task in the connected client after pairing.")
	if failures > 0 {
		return true, fmt.Errorf("doctor found %d missing local prerequisite(s)", failures)
	}
	return true, nil
}

func sessionGit(layout *paths.Layout) (runner.Runner, error) {
	env, err := settings.Environment(layout)
	if err != nil {
		return nil, err
	}
	return runner.NewExecRunner(append(env, "GIT_TERMINAL_PROMPT=0")...), nil
}
