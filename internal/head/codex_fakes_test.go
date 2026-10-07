package head

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/runner"
)

// fakeCodexUnit is codex-remote@operator.service as the fake system
// manager sees it.
type fakeCodexUnit struct {
	enabled bool
	active  bool
	// limitHit models systemd's start-rate limiter: a restart is refused
	// until reset-failed clears it.
	limitHit bool
	// noDaemon makes a start leave the oneshot active without a daemon
	// (the "active but unreachable" state).
	noDaemon bool
	// startFails makes the next restarts fail outright.
	startFails int
}

// codexSystem is a stateful fake of everything the Codex head runs through
// its System runner -- `sudo systemctl`, plain `systemctl` for the codex
// unit, the --user watchdog timer and `sudo journalctl` -- plus the codex
// CLI (as a second Runner, see cli). Any claude-remote@ call is forwarded to
// the Claude fake, so one runner can serve both heads as it does in
// production.
//
// The Codex daemon is modelled the way the probe observes the real one: a
// live daemon accepts a connection on the control socket (see listening).
type codexSystem struct {
	t      *testing.T
	claude *fakeSystemd
	home   string

	unit         fakeCodexUnit
	timerEnabled bool
	daemonPID    int
	nextPID      int
	spawns       int

	// timerFails makes every watchdog timer call fail.
	timerFails bool

	calls    [][]string
	cliCalls [][]string
}

func newCodexSystem(t *testing.T, claude *fakeSystemd, home string) *codexSystem {
	return &codexSystem{t: t, claude: claude, home: home, nextPID: 4242}
}

func (f *codexSystem) socketPath() string {
	return filepath.Join(f.home, ".codex", "app-server-control", "app-server-control.sock")
}

// startDaemon brings up a daemon owning the control socket.
func (f *codexSystem) startDaemon() {
	f.stopDaemon()
	f.nextPID++
	f.daemonPID = f.nextPID
	f.spawns++
}

func (f *codexSystem) stopDaemon() { f.daemonPID = 0 }

// listening is the head's control-socket probe: a live daemon listens, a dead
// one is refused.
func (f *codexSystem) listening(socket string) error {
	if socket != f.socketPath() {
		f.t.Fatalf("probe checked %s, want %s", socket, f.socketPath())
	}
	if f.daemonPID == 0 {
		return errors.New("connect: connection refused")
	}
	return nil
}

func (f *codexSystem) Run(name string, args ...string) (string, string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch name {
	case "sudo":
		return f.sudo(args)
	case "systemctl":
		if len(args) > 0 && args[0] == "--user" {
			if args[len(args)-1] == "codex-remote-watchdog.timer" {
				if f.timerFails {
					return "", "Unit codex-remote-watchdog.timer not found.", 5, fmt.Errorf("exit 5")
				}
				switch args[1] {
				case "enable":
					f.timerEnabled = true
				case "disable":
					f.timerEnabled = false
				}
				return "", "", 0, nil
			}
			return f.claude.Run(name, args...)
		}
		return f.systemctl(args, false)
	}
	return f.claude.Run(name, args...)
}

func (f *codexSystem) RunWithIO(name string, args []string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	out, _, code, err := f.Run(name, args...)
	if stdout != nil {
		_, _ = io.WriteString(stdout, out)
	}
	return code, err
}

func (f *codexSystem) sudo(args []string) (string, string, int, error) {
	switch args[0] {
	case "systemctl":
		return f.systemctl(args[1:], true)
	case "journalctl":
		return "-- journal for codex-remote@operator.service --\nError: relay down\n", "", 0, nil
	}
	f.t.Fatalf("unexpected sudo call %v", args)
	return "", "", 1, nil
}

const codexUnitName = "codex-remote@operator.service"

func (f *codexSystem) systemctl(args []string, root bool) (string, string, int, error) {
	verb := args[0]
	unit := args[len(args)-1]
	if verb == "show" {
		unit = args[1]
	}
	if unit != codexUnitName {
		f.t.Fatalf("system-scope systemctl touched %q (%v)", unit, args)
	}
	mutating := map[string]bool{"enable": true, "disable": true, "restart": true, "stop": true, "reset-failed": true}
	if mutating[verb] && !root {
		return "", "Access denied", 1, fmt.Errorf("exit 1")
	}
	u := &f.unit
	switch verb {
	case "is-enabled":
		if u.enabled {
			return "enabled\n", "", 0, nil
		}
		return "disabled\n", "", 1, fmt.Errorf("exit 1")
	case "is-active":
		if u.active {
			return "active\n", "", 0, nil
		}
		return "inactive\n", "", 3, fmt.Errorf("exit 3")
	case "show":
		state, sub := "inactive", "dead"
		if u.active {
			state, sub = "active", "exited"
		}
		return "ActiveState=" + state + "\nSubState=" + sub + "\n", "", 0, nil
	case "enable":
		u.enabled = true
	case "disable":
		u.enabled = false
		if len(args) == 3 && args[1] == "--now" {
			u.active = false
			f.stopDaemon()
		}
	case "stop":
		u.active = false
		f.stopDaemon()
	case "reset-failed":
		u.limitHit = false
	case "restart":
		if u.limitHit {
			return "", "Job for codex-remote@operator.service failed because the start rate limit was hit.", 1, fmt.Errorf("exit 1")
		}
		f.stopDaemon()
		if u.startFails > 0 {
			u.startFails--
			u.active = false
			return "", "Job for codex-remote@operator.service failed because the control process exited with error code.", 1, fmt.Errorf("exit 1")
		}
		u.active = true
		if !u.noDaemon {
			f.startDaemon()
		}
	default:
		f.t.Fatalf("unexpected codex systemctl verb %v", args)
	}
	return "", "", 0, nil
}

// cli is the codex CLI runner (CODEX_HOME scoped to the Remote home).
func (f *codexSystem) cli() runner.Runner { return codexCLI{f} }

type codexCLI struct{ f *codexSystem }

func (c codexCLI) Run(name string, args ...string) (string, string, int, error) {
	f := c.f
	f.cliCalls = append(f.cliCalls, append([]string{name}, args...))
	if name != "codex" {
		f.t.Fatalf("codex runner ran %s", name)
	}
	switch strings.Join(args, " ") {
	case "remote-control stop --json":
		f.stopDaemon()
		return "{\"status\":\"stopped\"}\n", "", 0, nil
	case "remote-control pair --json":
		if f.daemonPID == 0 {
			return "", "{\"status\":\"not_running\"}\n", 1, fmt.Errorf("exit 1")
		}
		return fmt.Sprintf("{\"status\":\"paired\",\"code\":\"ABCD-1234\",\"pid\":%d}\n", f.daemonPID), "", 0, nil
	}
	f.t.Fatalf("unexpected codex call %v", args)
	return "", "", 1, nil
}

func (c codexCLI) RunWithIO(name string, args []string, _ io.Reader, stdout, stderr io.Writer) (int, error) {
	out, errOut, code, err := c.Run(name, args...)
	_, _ = io.WriteString(stdout, out)
	_, _ = io.WriteString(stderr, errOut)
	return code, err
}

// callsWith returns the recorded System calls whose argv starts with
// prefix.
func (f *codexSystem) callsWith(prefix ...string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if len(c) >= len(prefix) && strings.Join(c[:len(prefix)], "\x00") == strings.Join(prefix, "\x00") {
			out = append(out, c)
		}
	}
	return out
}

// codexHost is a testHost with the Codex head wired over codexSystem.
type codexHost struct {
	*testHost
	sys   *codexSystem
	codex *Codex
	out   *bytes.Buffer
	now   time.Time
}

func newCodexHost(t *testing.T) *codexHost {
	t.Helper()
	h := newTestHost(t)
	ch := &codexHost{testHost: h, out: &bytes.Buffer{}, now: time.Unix(1_800_000_000, 0)}
	ch.sys = newCodexSystem(t, h.sd, h.layout.Home())
	codex, err := NewCodex(CodexOptions{
		Layout:    h.layout,
		System:    ch.sys,
		Codex:     ch.sys.cli(),
		Binary:    "codex",
		Operator:  "operator",
		Listening: ch.sys.listening,
		Out:       ch.out,
		Diag:      h.diag,
		Sleep:     func(time.Duration) { h.sleeps++ },
		Now:       func() time.Time { return ch.now },
	})
	if err != nil {
		t.Fatalf("NewCodex: %v", err)
	}
	ch.codex = codex
	return ch
}

// installCodex lays down what install gives a Codex host: the Remote home,
// the standalone executable and (with login) a non-empty auth.json.
func (h *codexHost) installCodex(login bool) {
	h.t.Helper()
	remote := CodexRemoteHome(h.layout)
	bin := filepath.Join(remote, "packages", "standalone", "current", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil { //nolint:gosec // fixture must be executable; never run
		h.t.Fatal(err)
	}
	if login {
		if err := os.WriteFile(filepath.Join(remote, "auth.json"), []byte("{\"fake\":true}\n"), 0o600); err != nil {
			h.t.Fatal(err)
		}
	}
}
