package head

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/systemd"
)

// The Codex head is one system unit per operator,
// codex-remote@<operator>.service, that turns on Codex Remote Control for
// the operator's ordinary Codex app-server daemon (~/.codex, shared with
// the interactive codex CLI). codvps adds no sandbox around it:
// the vendor's own daemon and per-command sandbox are what run. Unlike the
// Claude head it is not per repository.
//
// Liveness is never read from systemd alone: the unit is a oneshot that
// stays active(exited) after its daemon dies, so every "is it running"
// answer comes from the daemon's own control socket (see codexdaemon.go).

const (
	watchdogFailureWindow = 600 * time.Second
	watchdogMaxFailures   = 3
	// maxWatchdogStateBytes caps the failure log read: it is rewritten on
	// every success, so anything larger is not a log this code wrote.
	maxWatchdogStateBytes = 64 << 10
)

// CodexOptions are the Codex head's injected dependencies.
type CodexOptions struct {
	// Layout resolves HOME and the codvps state paths.
	Layout *paths.Layout
	// System runs systemctl (system and --user scope), sudo and journalctl.
	System runner.Runner
	// Codex runs the codex CLI with its default CODEX_HOME.
	Codex runner.Runner
	// Binary is the Codex CLI's executable name (the codex provider's
	// Binary), run through Codex and found in the standalone install.
	Binary string
	// Operator is the account whose codex-remote@<operator> unit this is.
	Operator string
	// Listening reports whether a daemon listens on the control socket,
	// without connecting to it; ControlSocketListening in production.
	Listening func(socket string) error
	// Out receives command output meant for the operator (pairing JSON).
	Out io.Writer
	// Diag receives warnings, journal excerpts and watchdog log lines.
	Diag io.Writer
	// Sleep waits between activation polls; tests pass a no-op.
	Sleep func(time.Duration)
	// Now is the watchdog's clock.
	Now func() time.Time
}

// Codex manages the host-wide Codex head.
type Codex struct {
	layout    *paths.Layout
	sys       runner.Runner
	cli       runner.Runner
	binary    string
	operator  string
	listening func(string) error
	out       io.Writer
	diag      io.Writer
	sleep     func(time.Duration)
	now       func() time.Time
}

// NewCodex builds a Codex head from opts; every dependency is required.
func NewCodex(opts CodexOptions) (*Codex, error) {
	switch {
	case opts.Layout == nil:
		return nil, errors.New("codex head: Layout is required")
	case opts.System == nil:
		return nil, errors.New("codex head: System runner is required")
	case opts.Codex == nil:
		return nil, errors.New("codex head: Codex runner is required")
	case opts.Binary == "":
		return nil, errors.New("codex head: Binary is required")
	case opts.Operator == "":
		return nil, errors.New("codex head: Operator is required")
	case opts.Listening == nil:
		return nil, errors.New("codex head: Listening is required")
	case opts.Out == nil || opts.Diag == nil:
		return nil, errors.New("codex head: Out and Diag writers are required")
	case opts.Sleep == nil || opts.Now == nil:
		return nil, errors.New("codex head: Sleep and Now are required")
	}
	return &Codex{
		layout: opts.Layout, sys: opts.System, cli: opts.Codex, binary: opts.Binary,
		operator: opts.Operator, listening: opts.Listening,
		out: opts.Out, diag: opts.Diag, sleep: opts.Sleep, now: opts.Now,
	}, nil
}

// CodexRemoteDir is the Codex home Codex Remote runs from, relative to the
// operator's home: Codex's own default, shared with the interactive CLI,
// so both see one install, one login and the same threads.
const CodexRemoteDir = ".codex"

// CodexRemoteHome is the Codex Remote state directory.
func CodexRemoteHome(layout *paths.Layout) string {
	return filepath.Join(layout.Home(), CodexRemoteDir)
}

// CodexStandalone is the standalone install's own current executable
// under a Codex home: the unpinned fallback the Codex head runs.
func CodexStandalone(codexHome, binary string) string {
	return filepath.Join(codexHome, "packages", "standalone", "current", binary)
}

func (c *Codex) unit() string       { return systemd.CodexUnit(c.operator) }
func (c *Codex) remoteHome() string { return CodexRemoteHome(c.layout) }

func (c *Codex) controlSocket() string { return ControlSocket(c.remoteHome()) }

// errNeedsStandaloneText is the missing-standalone refusal. The standalone
// Codex is a vendor download, which the codvps bootstrap installer
// performs; `codvps install` itself only wires up what is already on disk.
const errNeedsStandaloneText = "Codex Remote requires the Codex standalone install under ~/.codex; codvps enable codex installs it"

var (
	errNeedsCodexLogin = errors.New("run codvps login codex first")
	errCodexDisabled   = errors.New("Codex Remote head is disabled; run codvps enable codex first")
)

// SetEnabled enables or disables the Codex head (the reference's
// codex_head_set_enabled). Enabling requires a Codex login and the
// standalone install, removes a control-socket link left by the retired
// sandboxed unit, then enables and restarts the unit and requires both the
// unit and its daemon to come up before turning the watchdog on. Disabling
// turns the watchdog off, stops and disables the unit, and runs Codex's own
// remote-control stop.
func (c *Codex) SetEnabled(enable bool) error {
	if !enable {
		return c.disable()
	}
	if err := c.requireCredential(); err != nil {
		return err
	}
	if ok, err := c.standalonePresent(); err != nil {
		return err
	} else if !ok {
		return errors.New(errNeedsStandaloneText)
	}
	if _, err := RemoveDanglingControlSocket(c.remoteHome()); err != nil {
		return err
	}
	if err := c.sudoSystemctl("enable", c.unit()); err != nil {
		return err
	}
	if err := c.managedRestart(); err != nil {
		return err
	}
	if err := c.waitForActive(); err != nil {
		return err
	}
	if running, reason := c.probeDaemon(); !running {
		return c.unavailableAfterStart(reason)
	}
	if _, stderr, code, err := c.sys.Run("systemctl", "--user", "enable", "--now", systemd.CodexWatchdogTimer); code != 0 || err != nil {
		// The head itself is up; a missing watchdog only loses automatic
		// recovery, so this is a warning, as in the reference.
		c.diagf("codvps: warning: could not enable watchdog timer %s: %s\n", systemd.CodexWatchdogTimer, describeFailure(stderr, code, err))
	}
	return nil
}

func (c *Codex) disable() error {
	// Ignored deliberately: the timer may never have been enabled (or
	// installed) on this host, and the head below must be switched off
	// regardless.
	_, _, _, _ = c.sys.Run("systemctl", "--user", "disable", "--now", systemd.CodexWatchdogTimer)
	if err := c.sudoSystemctl("disable", "--now", c.unit()); err != nil {
		return err
	}
	c.stopDaemon()
	return nil
}

// Pair prints a short-lived pairing code as JSON (`codex remote-control
// pair --json`), but only for the codvps-managed head: the unit must be
// enabled and active and its daemon reachable. An unmanaged daemon started
// by hand is refused.
func (c *Codex) Pair() error {
	if err := c.requireCredential(); err != nil {
		return err
	}
	if !c.quiet("is-enabled") || !c.quiet("is-active") {
		return errCodexDisabled
	}
	if running, reason := c.probeDaemon(); !running {
		return fmt.Errorf("Codex Remote daemon is unavailable (%s); run codvps enable codex first", reason)
	}
	code, err := c.cli.RunWithIO(c.binary, []string{"remote-control", "pair", "--json"}, nil, c.out, c.diag)
	if code != 0 || err != nil {
		return fmt.Errorf("%s remote-control pair --json failed: %s", c.binary, describeFailure("", code, err))
	}
	return nil
}

// Ensure is the watchdog's `head ensure codex`: a no-op while the daemon
// answers on its control socket; otherwise, unless it already failed three
// times in the last ten minutes or the head is disabled, one managed
// restart. A failed restart is recorded for the backoff and returned.
func (c *Codex) Ensure() error {
	failures := filepath.Join(c.layout.StateDir(), "codex-watchdog.failures")
	if running, _ := c.probeDaemon(); running {
		return clearFailures(failures)
	}

	recent, err := c.recentFailures(failures)
	if err != nil {
		return err
	}
	if recent >= watchdogMaxFailures {
		c.journal(4, fmt.Sprintf("codex daemon unavailable; backing off after %d failures in %ds", recent, int(watchdogFailureWindow/time.Second)))
		return nil
	}
	if !c.quiet("is-enabled") {
		c.journal(6, "codex unit disabled; skipping restart")
		return nil
	}

	c.journal(6, "codex daemon unavailable; attempting restart")
	if err := c.managedRestart(); err != nil {
		return c.recordFailure(failures, "codex restart failed", err)
	}
	if err := c.waitForActive(); err != nil {
		return c.recordFailure(failures, "codex restart failed", err)
	}
	if running, reason := c.probeDaemon(); !running {
		return c.recordFailure(failures, "codex unit active but daemon not reachable", c.unavailableAfterStart(reason))
	}
	return clearFailures(failures)
}

// DaemonRunning reports whether the Codex Remote daemon answers on its
// control socket.
func (c *Codex) DaemonRunning() (bool, error) {
	running, _ := c.probeDaemon()
	return running, nil
}

// DaemonStatus is DaemonRunning plus, when it is not running, why.
func (c *Codex) DaemonStatus() (bool, string, error) {
	running, reason := c.probeDaemon()
	return running, reason, nil
}

// EnabledState is the raw `systemctl is-enabled` answer for the unit. The
// exit status is not an error: is-enabled exits non-zero for a perfectly
// valid "disabled", and "" (no answer) is rendered as unknown by callers.
func (c *Codex) EnabledState() (string, error) {
	out, _, _, _ := c.sys.Run("systemctl", "is-enabled", c.unit())
	return strings.TrimSpace(out), nil
}

// UnitState is the unit's ActiveState and SubState; empty when systemd
// gave no answer, which callers render as not-found/-.
func (c *Codex) UnitState() (string, string, error) {
	props := repo.ShowUnitProperties(c.sys, false, c.unit(), "ActiveState", "SubState")
	return props["ActiveState"], props["SubState"], nil
}

// requireCredential is the reference's has_codex_cred: a non-empty
// ~/.codex/auth.json.
func (c *Codex) requireCredential() error {
	fi, err := os.Stat(filepath.Join(c.remoteHome(), "auth.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errNeedsCodexLogin
		}
		return fmt.Errorf("failed to check the Codex credential: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return errNeedsCodexLogin
	}
	return nil
}

// standalonePresent reports whether the standalone Codex the unit's
// ExecStop names exists and is executable.
func (c *Codex) standalonePresent() (bool, error) {
	fi, err := os.Stat(CodexStandalone(c.remoteHome(), c.binary))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check the standalone Codex install: %w", err)
	}
	return fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0, nil
}

// managedRestart is the reference's start_codex_system_unit restart. It
// clears systemd's failed state and start-rate counter first, so explicit
// recovery attempts close together are not refused with start-limit-hit;
// the unit has no Restart= policy, so this cannot create a restart loop.
func (c *Codex) managedRestart() error {
	if err := c.sudoSystemctl("reset-failed", c.unit()); err != nil {
		return err
	}
	return c.sudoSystemctl("restart", c.unit())
}

// waitForActive polls the unit until it has been active twice in a row.
// On timeout it copies the unit's recent journal to Diag and fails; unlike
// a Claude head there is no restart loop to break (no Restart=), so the
// unit is left as systemd reports it.
func (c *Codex) waitForActive() error {
	consecutive := 0
	for attempt := 1; attempt <= activationAttempts; attempt++ {
		if c.quiet("is-active") {
			consecutive++
			if consecutive >= requiredConsecutiveActive {
				return nil
			}
		} else {
			consecutive = 0
		}
		c.sleep(activationInterval)
	}
	stdout, stderr, code, err := c.sys.Run("sudo", "journalctl", "-u", c.unit(), "-n", journalLines, "--no-pager")
	if code != 0 || err != nil {
		c.diagf("codvps: could not read the journal for %s: %s\n", c.unit(), describeFailure(stderr, code, err))
	} else {
		c.diagf("%s", stdout)
	}
	return errors.New("Codex Remote Control failed to become active")
}

func (c *Codex) unavailableAfterStart(reason string) error {
	return fmt.Errorf("Codex Remote unit is active but its daemon is unavailable (%s); inspect: sudo journalctl -u %s", reason, c.unit())
}

// stopDaemon runs Codex's own `remote-control stop`, which stops the
// app-server daemon for ~/.codex. That daemon is shared with the
// interactive codex CLI, so this also ends it. Its failure is ignored
// deliberately: with no daemon (or no codex CLI on PATH) there is nothing
// to stop, and the managed unit was already stopped with its errors checked.
func (c *Codex) stopDaemon() {
	_, _, _, _ = c.cli.Run(c.binary, "remote-control", "stop", "--json")
}

// quiet runs `systemctl <verb> --quiet <unit>` (system scope, no sudo:
// reading unit state needs no privilege) and reports its exit status.
func (c *Codex) quiet(verb string) bool {
	_, _, code, err := c.sys.Run("systemctl", verb, "--quiet", c.unit())
	return code == 0 && err == nil
}

func (c *Codex) sudoSystemctl(args ...string) error {
	_, stderr, code, err := c.sys.Run("sudo", append([]string{"systemctl"}, args...)...)
	if code != 0 || err != nil {
		return fmt.Errorf("sudo systemctl %s: %s", strings.Join(args, " "), describeFailure(stderr, code, err))
	}
	return nil
}

// diagf writes to Diag (stderr). A failed write has nowhere left to be
// reported, and the command's own result still reaches the operator
// through its exit status, so it is discarded.
func (c *Codex) diagf(format string, args ...any) {
	_, _ = fmt.Fprintf(c.diag, format, args...)
}

// journal writes one watchdog log line with a syslog priority prefix,
// which systemd's journal (StandardError=journal) turns into that entry's
// priority: 3 err, 4 warning, 6 info.
func (c *Codex) journal(priority int, msg string) {
	c.diagf("<%d>codvps-watchdog: %s\n", priority, msg)
}

// recentFailures counts recorded restart failures inside the backoff
// window. Lines that are not timestamps are ignored, as the reference's
// arithmetic ignores them.
func (c *Codex) recentFailures(path string) (int, error) {
	data, err := readCapped(path, maxWatchdogStateBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read the Codex watchdog state %s: %w", path, err)
	}
	now := c.now().Unix()
	recent := 0
	for _, line := range strings.Split(string(data), "\n") {
		ts, err := strconv.ParseInt(strings.TrimSpace(line), 10, 64)
		if err != nil {
			continue
		}
		if now-ts < int64(watchdogFailureWindow/time.Second) {
			recent++
		}
	}
	return recent, nil
}

// recordFailure appends one timestamp to the watchdog's failure log, logs
// msg at err priority and returns cause (joined with any failure to
// record it).
func (c *Codex) recordFailure(path, msg string, cause error) error {
	c.journal(3, msg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return errors.Join(cause, fmt.Errorf("failed to record the watchdog failure: %w", err))
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("failed to record the watchdog failure: %w", err))
	}
	_, werr := fmt.Fprintf(f, "%d\n", c.now().Unix())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return errors.Join(cause, fmt.Errorf("failed to record the watchdog failure: %w", werr))
	}
	return cause
}

func clearFailures(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to clear the Codex watchdog state %s: %w", path, err)
	}
	return nil
}

// readCapped reads at most limit bytes of path and fails on a larger file.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // read-only: a close error loses nothing
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	return data, nil
}
