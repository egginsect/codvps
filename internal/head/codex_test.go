package head

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func joinCalls(calls [][]string) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// indexOf returns the position of the first call equal to want, or -1.
func indexOf(calls [][]string, want string) int {
	for i, c := range joinCalls(calls) {
		if c == want {
			return i
		}
	}
	return -1
}

// Enable refreshes nothing but systemd: no mount policy, no manifest.
// It enables and restarts the unit, waits for two active polls,
// confirms the daemon on its control socket, spawns exactly one daemon and
// turns the watchdog on.
func TestCodexEnable_StartsTheManagedHead(t *testing.T) {
	h := newCodexHost(t)
	h.register("codex-one", "codex-two")
	h.installCodex(true)

	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v\n%s", err, h.diag)
	}
	if _, err := os.Lstat(h.layout.CodexRepositoriesPath()); !os.IsNotExist(err) {
		t.Fatalf("enable wrote a Codex membership manifest: %v", err)
	}

	calls := h.sys.calls
	enable := indexOf(calls, "sudo systemctl enable codex-remote@operator.service")
	reset := indexOf(calls, "sudo systemctl reset-failed codex-remote@operator.service")
	restart := indexOf(calls, "sudo systemctl restart codex-remote@operator.service")
	timer := indexOf(calls, "systemctl --user enable --now codex-remote-watchdog.timer")
	if enable < 0 || enable >= reset || reset >= restart || restart >= timer {
		t.Fatalf("enable order wrong (enable=%d reset=%d restart=%d timer=%d):\n%s",
			enable, reset, restart, timer, strings.Join(joinCalls(calls), "\n"))
	}
	for _, c := range joinCalls(calls) {
		if strings.Contains(c, "isolation") {
			t.Fatalf("enable ran the retired isolation step: %s", c)
		}
	}
	if !h.sys.unit.enabled || !h.sys.unit.active || h.sys.daemonPID == 0 || !h.sys.timerEnabled {
		t.Fatalf("head not up: unit=%+v daemon=%d timer=%v", h.sys.unit, h.sys.daemonPID, h.sys.timerEnabled)
	}
	if h.sys.spawns != 1 {
		t.Fatalf("daemon spawned %d times, want exactly 1", h.sys.spawns)
	}
	if len(h.sys.callsWith("systemctl", "is-active", "--quiet", codexUnitName)) != requiredConsecutiveActive {
		t.Fatalf("activation was not confirmed by two consecutive polls")
	}
}

// The retired sandboxed unit left the control socket as a symlink into its
// private /tmp. Enable removes that link, and only that: a link that
// resolves, a live socket path or a regular file is left alone.
func TestCodexEnable_RemovesADanglingControlSocketLink(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	socket := h.sys.socketPath()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp/codex-private-tmp-gone/app-server-control.sock", socket); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true): %v\n%s", err, h.diag)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the dangling control socket link survived enable: %v", err)
	}
}

func TestRemoveDanglingControlSocket(t *testing.T) {
	home := t.TempDir()
	socket := ControlSocket(home)
	if removed, err := RemoveDanglingControlSocket(home); err != nil || removed {
		t.Fatalf("nothing there = (%v, %v)", removed, err)
	}
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}

	// A regular file at the path is not ours to remove.
	if err := os.WriteFile(socket, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveDanglingControlSocket(home); err != nil || removed || DanglingControlSocket(home) {
		t.Fatalf("regular file = (%v, %v)", removed, err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}

	// A link whose target exists is left alone.
	target := filepath.Join(home, "real.sock")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, socket); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveDanglingControlSocket(home); err != nil || removed || DanglingControlSocket(home) {
		t.Fatalf("resolving link = (%v, %v)", removed, err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}

	// A link to nowhere is removed.
	if err := os.Symlink(filepath.Join(home, "gone", "app-server-control.sock"), socket); err != nil {
		t.Fatal(err)
	}
	if !DanglingControlSocket(home) {
		t.Fatal("a link to nowhere was not reported dangling")
	}
	if removed, err := RemoveDanglingControlSocket(home); err != nil || !removed {
		t.Fatalf("dangling link = (%v, %v)", removed, err)
	}
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("the dangling link survived: %v", err)
	}
}

// Reference scenario test_codex_disable_enable_and_credential_gate: enable
// without a (non-empty) Codex Remote auth.json is refused before anything
// runs; disable stops the unit and its daemon and the watchdog; a re-enable
// starts a fresh daemon.
func TestCodexEnableDisable_CredentialGate(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(false)
	if err := h.codex.SetEnabled(true); !errors.Is(err, errNeedsCodexLogin) || err.Error() != "run codvps login codex first" {
		t.Fatalf("enable without auth.json = %v", err)
	}
	if err := os.WriteFile(filepath.Join(CodexRemoteHome(h.layout), "auth.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.SetEnabled(true); !errors.Is(err, errNeedsCodexLogin) {
		t.Fatalf("enable with an empty auth.json = %v", err)
	}
	if len(h.sys.calls) != 0 {
		t.Fatalf("a refused enable ran %v", h.sys.calls)
	}

	h.installCodex(true)
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	oldPID := h.sys.daemonPID

	if err := h.codex.SetEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if h.sys.unit.active || h.sys.unit.enabled || h.sys.daemonPID != 0 || h.sys.timerEnabled {
		t.Fatalf("disable left state behind: unit=%+v daemon=%d timer=%v", h.sys.unit, h.sys.daemonPID, h.sys.timerEnabled)
	}
	if indexOf(h.sys.calls, "sudo systemctl disable --now codex-remote@operator.service") < 0 {
		t.Fatalf("disable did not stop and disable the unit as root")
	}
	if indexOf(h.sys.cliCalls, "codex remote-control stop --json") < 0 {
		t.Fatalf("disable did not run Codex's own remote-control stop")
	}

	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if h.sys.daemonPID == 0 || h.sys.daemonPID == oldPID {
		t.Fatalf("re-enable did not start a new daemon (old %d, new %d)", oldPID, h.sys.daemonPID)
	}
}

func TestCodexEnable_RequiresTheStandaloneInstall(t *testing.T) {
	h := newCodexHost(t)
	if err := os.MkdirAll(CodexRemoteHome(h.layout), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(CodexRemoteHome(h.layout), "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := h.codex.SetEnabled(true)
	if err == nil || err.Error() != errNeedsStandaloneText {
		t.Fatalf("enable without the standalone codex = %v", err)
	}
	if len(h.sys.calls) != 0 {
		t.Fatalf("a refused enable ran %v", h.sys.calls)
	}
}

// A disable must propagate a unit that cannot be stopped, but not the
// best-effort watchdog/unmanaged-daemon steps.
func TestCodexDisable_PropagatesOnlyTheUnitFailure(t *testing.T) {
	h := newCodexHost(t)
	h.sys.timerFails = true
	if err := h.codex.SetEnabled(false); err != nil {
		t.Fatalf("disable with nothing running: %v", err)
	}

	bad := &failingSudo{codexSystem: h.sys}
	h.codex.sys = bad
	err := h.codex.SetEnabled(false)
	if err == nil || !strings.Contains(err.Error(), "sudo systemctl disable --now codex-remote@operator.service: sudo: a password is required") {
		t.Fatalf("disable with a failing sudo = %v", err)
	}
}

// failingSudo refuses every sudo call, as a host without the operator's
// sudo rights would.
type failingSudo struct{ *codexSystem }

func (f *failingSudo) Run(name string, args ...string) (string, string, int, error) {
	if name == "sudo" {
		return "", "sudo: a password is required", 1, errors.New("exit 1")
	}
	return f.codexSystem.Run(name, args...)
}

// Reference scenario test_codex_head_starts_without_claude_credential: the
// Codex head never needs a Claude login.
func TestCodexEnable_StartsWithoutAClaudeCredential(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	if _, err := os.Stat(h.layout.ClaudeCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has a Claude credential: %v", err)
	}
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("enable without a Claude credential: %v", err)
	}
	if h.sys.daemonPID == 0 {
		t.Fatalf("Codex did not start without a Claude credential")
	}
}

// Reference scenario test_codex_managed_start_recovers_systemd_rate_limit:
// a unit stuck in start-limit-hit is recovered by the managed start, which
// clears the failed/rate state right before its one explicit restart.
func TestCodexEnable_RecoversTheSystemdRateLimit(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	h.sys.unit.limitHit = true
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("enable with the start limit hit: %v", err)
	}
	if !h.sys.unit.active || h.sys.daemonPID == 0 {
		t.Fatalf("the managed start did not recover the unit")
	}
	if len(h.sys.callsWith("sudo", "systemctl", "restart")) != 1 {
		t.Fatalf("expected exactly one explicit restart: %v", joinCalls(h.sys.calls))
	}
}

// The unit comes up but its daemon never answers: enable fails, naming the
// probe's reason and where to look, and leaves the watchdog off.
func TestCodexEnable_FailsWhenTheDaemonIsUnreachable(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	h.sys.unit.noDaemon = true
	err := h.codex.SetEnabled(true)
	want := "Codex Remote unit is active but its daemon is unavailable (cannot connect to the app-server control socket " +
		h.sys.socketPath() + ": connect: connection refused); inspect: sudo journalctl -u codex-remote@operator.service"
	if err == nil || err.Error() != want {
		t.Fatalf("enable = %v\nwant %s", err, want)
	}
	if h.sys.timerEnabled {
		t.Fatalf("the watchdog was enabled for a head that did not come up")
	}
}

// A unit that never becomes active fails the enable with its journal on
// Diag after the reference's 30 polls.
func TestCodexEnable_FailsWhenTheUnitNeverBecomesActive(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	h.sys.unit.startFails = 1
	err := h.codex.SetEnabled(true)
	if err == nil || !strings.Contains(err.Error(), "restart codex-remote@operator.service: Job for codex-remote@operator.service failed") {
		t.Fatalf("enable with a failing restart = %v", err)
	}

	h2 := newCodexHost(t)
	h2.installCodex(true)
	h2.codex.sys = &neverActive{codexSystem: h2.sys}
	err = h2.codex.SetEnabled(true)
	if err == nil || err.Error() != "Codex Remote Control failed to become active" {
		t.Fatalf("enable with a unit that never activates = %v", err)
	}
	if h2.sleeps != activationAttempts || !strings.Contains(h2.diag.String(), "Error: relay down") {
		t.Fatalf("polls=%d diag=%q", h2.sleeps, h2.diag.String())
	}
}

// neverActive reports the unit inactive on every poll.
type neverActive struct{ *codexSystem }

func (f *neverActive) Run(name string, args ...string) (string, string, int, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
		return "", "", 3, errors.New("exit 3")
	}
	return f.codexSystem.Run(name, args...)
}

// Reference scenario test_codex_head_list_status_and_pair: pair delegates
// to `codex remote-control pair --json` for the managed head and prints the
// session-hook note; list/status read liveness from the daemon's socket, so
// an active oneshot whose daemon died reads unavailable with the reason,
// and re-enabling repairs it with a new daemon.
func TestCodexPairListAndStatus(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.Pair(); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	out := h.out.String()
	if !strings.Contains(out, `"status":"paired"`) || strings.Contains(out, "session hook") {
		t.Fatalf("pair output = %q", out)
	}
	if indexOf(h.sys.cliCalls, "codex remote-control pair --json") < 0 {
		t.Fatalf("pair did not delegate to codex: %v", h.sys.cliCalls)
	}

	var list strings.Builder
	if err := RenderList(&list, testHeads(h.claude, h.codex)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), row(headListRow, "codex", "host", "-", "enabled", "running")) {
		t.Fatalf("head list = %s", list.String())
	}

	// The daemon dies behind the still-active oneshot.
	dead := h.sys.daemonPID
	h.sys.stopDaemon()
	list.Reset()
	if err := RenderList(&list, testHeads(h.claude, h.codex)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.String(), row(headListRow, "codex", "host", "-", "enabled", "unavailable")) {
		t.Fatalf("head list with a dead daemon = %s", list.String())
	}
	var status strings.Builder
	if err := RenderStatus(&status, StatusOptions{Heads: testHeads(h.claude, h.codex), Interactive: testInteractive, Tools: h.sd, Operator: "operator"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.String(), row(statusRow, "codex", "active", "exited", "unavailable")) ||
		!strings.Contains(status.String(), "  (daemon unreachable from this session: cannot connect to the app-server control socket") {
		t.Fatalf("status with a dead daemon = %s", status.String())
	}
	err := h.codex.Pair()
	if err == nil || !strings.HasPrefix(err.Error(), "Codex Remote daemon is unavailable (cannot connect to the app-server control socket") {
		t.Fatalf("pair with a dead daemon = %v", err)
	}

	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if h.sys.daemonPID == 0 || h.sys.daemonPID == dead {
		t.Fatalf("re-enable did not repair the daemon")
	}
}

// An unmanaged daemon (started by hand against the Remote home) does not
// make pairing succeed while the head is disabled; neither does a missing
// Codex login.
func TestCodexPair_RefusesAnUnmanagedDaemon(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	h.sys.startDaemon()
	if err := h.codex.Pair(); !errors.Is(err, errCodexDisabled) || !strings.Contains(err.Error(), "head is disabled") {
		t.Fatalf("pair with an unmanaged daemon = %v", err)
	}
	if len(h.sys.cliCalls) != 0 {
		t.Fatalf("pair reached codex: %v", h.sys.cliCalls)
	}
	if err := os.Remove(filepath.Join(CodexRemoteHome(h.layout), "auth.json")); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.Pair(); !errors.Is(err, errNeedsCodexLogin) {
		t.Fatalf("pair without a login = %v", err)
	}
}

// The production probe reads the kernel's unix socket table instead of
// connecting (a connection without a WebSocket handshake makes the daemon
// log a warning each time): only a listening stream socket bound to the
// link's resolved target counts.
func TestControlSocketListening(t *testing.T) {
	const (
		listen = "00000000: 00000002 00000000 00010000 0001 01 12345 "
		idle   = "00000000: 00000002 00000000 00000000 0001 03 12346 "
	)
	dir := t.TempDir()
	real := filepath.Join(dir, "real.sock")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	probe := func(table, socket string) error {
		proc := t.TempDir()
		if err := os.MkdirAll(filepath.Join(proc, "net"), 0o755); err != nil {
			t.Fatal(err)
		}
		header := "Num       RefCount Protocol Flags    Type St Inode Path\n"
		if err := os.WriteFile(filepath.Join(proc, "net", "unix"), []byte(header+table), 0o600); err != nil {
			t.Fatal(err)
		}
		return controlSocketListening(proc, socket)
	}

	if err := probe(listen+real+"\n", link); err != nil {
		t.Fatalf("listening socket behind the link: %v", err)
	}
	if err := probe(listen+real+"\n", real); err != nil {
		t.Fatalf("listening socket: %v", err)
	}
	if err := probe(idle+real+"\n", link); err == nil {
		t.Fatal("a bound but not listening socket counted as live")
	}
	if err := probe(listen+"/elsewhere.sock\n"+listen+"\n", link); err == nil {
		t.Fatal("a listener on another path counted as live")
	}
	dangling := filepath.Join(dir, "dangling.sock")
	if err := os.Symlink(filepath.Join(dir, "gone.sock"), dangling); err != nil {
		t.Fatal(err)
	}
	if err := probe(listen+real+"\n", dangling); err == nil {
		t.Fatal("a dangling link counted as live")
	}
	if err := probe(listen+real+"\n", filepath.Join(dir, "missing.sock")); err == nil {
		t.Fatal("a missing socket counted as live")
	}
}

// The head's probe reports the daemon live or the precise reason it is not.
func TestCodexDaemonProbe(t *testing.T) {
	h := newCodexHost(t)
	socket := h.sys.socketPath()

	want := "cannot connect to the app-server control socket " + socket + ": connect: connection refused"
	if running, reason := h.codex.probeDaemon(); running || reason != want {
		t.Fatalf("no daemon: (%v, %q), want reason %q", running, reason, want)
	}
	h.sys.startDaemon()
	if running, reason := h.codex.probeDaemon(); !running || reason != "" {
		t.Fatalf("live daemon not detected: (%v, %q)", running, reason)
	}
}

// head ensure codex (the watchdog): a live daemon is a no-op that clears
// the failure log; a dead one on an enabled head gets one managed restart;
// a disabled head is left alone; a restart that does not bring the daemon
// back is recorded and, after three in ten minutes, the watchdog backs off.
func TestCodexEnsure_Watchdog(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	failures := filepath.Join(h.layout.StateDir(), "codex-watchdog.failures")

	// Disabled head, dead daemon: skip.
	if err := h.codex.Ensure(); err != nil {
		t.Fatalf("Ensure on a disabled head: %v", err)
	}
	if len(h.sys.callsWith("sudo")) != 0 || !strings.Contains(h.diag.String(), "<6>codvps-watchdog: codex unit disabled; skipping restart") {
		t.Fatalf("disabled head was touched: %v\n%s", joinCalls(h.sys.calls), h.diag)
	}

	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	h.sys.calls = nil
	if err := os.MkdirAll(filepath.Dir(failures), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failures, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.Ensure(); err != nil {
		t.Fatalf("Ensure with a live daemon: %v", err)
	}
	if len(h.sys.callsWith("sudo")) != 0 {
		t.Fatalf("a live daemon was restarted: %v", joinCalls(h.sys.calls))
	}
	if _, err := os.Stat(failures); !os.IsNotExist(err) {
		t.Fatalf("a live daemon did not clear the failure log: %v", err)
	}

	// The daemon dies: one managed restart brings it back.
	h.sys.stopDaemon()
	if err := h.codex.Ensure(); err != nil {
		t.Fatalf("Ensure after the daemon died: %v", err)
	}
	if h.sys.daemonPID == 0 || indexOf(h.sys.calls, "sudo systemctl reset-failed codex-remote@operator.service") < 0 {
		t.Fatalf("the watchdog did not restart the head: %v", joinCalls(h.sys.calls))
	}

	// Restarts that leave the daemon unreachable are recorded, then backed off.
	h.sys.unit.noDaemon = true
	h.sys.stopDaemon()
	for i := 1; i <= watchdogMaxFailures; i++ {
		err := h.codex.Ensure()
		if err == nil || !strings.Contains(err.Error(), "its daemon is unavailable") {
			t.Fatalf("failed restart %d = %v", i, err)
		}
		h.now = h.now.Add(10 * 1e9)
	}
	data, err := os.ReadFile(failures)
	if err != nil || len(strings.Fields(string(data))) != watchdogMaxFailures {
		t.Fatalf("failure log = %q (%v)", data, err)
	}
	h.sys.calls = nil
	if err := h.codex.Ensure(); err != nil {
		t.Fatalf("backed-off Ensure: %v", err)
	}
	if len(h.sys.callsWith("sudo")) != 0 || !strings.Contains(h.diag.String(), "<4>codvps-watchdog: codex daemon unavailable; backing off after 3 failures in 600s") {
		t.Fatalf("the watchdog did not back off: %v\n%s", joinCalls(h.sys.calls), h.diag)
	}

	// Failures older than the window no longer count.
	h.now = h.now.Add(watchdogFailureWindow)
	h.sys.unit.noDaemon = false
	if err := h.codex.Ensure(); err != nil {
		t.Fatalf("Ensure after the window: %v", err)
	}
	if h.sys.daemonPID == 0 {
		t.Fatalf("the watchdog did not retry after the backoff window")
	}
	if _, err := os.Stat(failures); !os.IsNotExist(err) {
		t.Fatalf("a successful restart did not clear the failure log: %v", err)
	}
}

// A restart systemd refuses is recorded like any other failed attempt.
func TestCodexEnsure_RecordsARefusedRestart(t *testing.T) {
	h := newCodexHost(t)
	h.installCodex(true)
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	h.sys.stopDaemon()
	h.sys.unit.startFails = 1
	if err := h.codex.Ensure(); err == nil {
		t.Fatal("Ensure hid a failed restart")
	}
	data, err := os.ReadFile(filepath.Join(h.layout.StateDir(), "codex-watchdog.failures"))
	if err != nil || strings.TrimSpace(string(data)) != strconv.FormatInt(h.now.Unix(), 10) {
		t.Fatalf("failure log = %q (%v)", data, err)
	}
	if !strings.Contains(h.diag.String(), "<3>codvps-watchdog: codex restart failed") {
		t.Fatalf("failure not logged: %s", h.diag)
	}
}

// Codex Remote runs from Codex's own default home, ~/.codex, shared with
// the interactive CLI (the reference kept a separate
// ~/.codex-remote, which codvps no longer uses or creates).
func TestCodexRemoteSharesTheCodexHome(t *testing.T) {
	h := newCodexHost(t)
	if got, want := CodexRemoteHome(h.layout), filepath.Join(h.layout.Home(), ".codex"); got != want {
		t.Fatalf("CodexRemoteHome = %q, want %q", got, want)
	}
	h.installCodex(true)
	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := h.codex.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(h.layout.Home(), ".codex-remote")); !os.IsNotExist(err) {
		t.Fatalf("a head operation created ~/.codex-remote: %v", err)
	}
}

// failingCLI is a codex CLI that cannot run.
type failingCLI struct{}

func (failingCLI) Run(string, ...string) (string, string, int, error) {
	return "", "", 127, errors.New(`exec: "codex": executable file not found in $PATH`)
}

func (failingCLI) RunWithIO(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 127, errors.New(`exec: "codex": executable file not found in $PATH`)
}

// Disable ignores a codex CLI that cannot run: with nothing to stop, the
// managed unit's stop is what counts.
func TestCodexDisable_IgnoresAMissingCodexCLI(t *testing.T) {
	h := newCodexHost(t)
	h.codex.cli = failingCLI{}
	if err := h.codex.SetEnabled(false); err != nil {
		t.Fatalf("a failing best-effort codex stop must be ignored: %v", err)
	}
}

// EnabledState is systemd's raw answer whatever the exit status; UnitState
// is the show properties, empty when systemd gives no answer.
func TestCodexEnabledAndUnitState(t *testing.T) {
	h := newCodexHost(t)
	if st, err := h.codex.EnabledState(); err != nil || st != "disabled" {
		t.Fatalf("EnabledState = (%q, %v), want disabled despite exit 1", st, err)
	}
	h.sys.unit = fakeCodexUnit{enabled: true, active: true}
	if st, _ := h.codex.EnabledState(); st != "enabled" {
		t.Fatalf("EnabledState = %q", st)
	}
	if a, s, err := h.codex.UnitState(); err != nil || a != "active" || s != "exited" {
		t.Fatalf("UnitState = (%q, %q, %v)", a, s, err)
	}
	h.codex.sys = failingTools{}
	if a, s, err := h.codex.UnitState(); err != nil || a != "" || s != "" {
		t.Fatalf("UnitState with no answer = (%q, %q, %v)", a, s, err)
	}
	if st, err := h.codex.EnabledState(); err != nil || st != "" {
		t.Fatalf("EnabledState with no answer = (%q, %v)", st, err)
	}
}
