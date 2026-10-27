package cli

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/session"
	"github.com/egginsect/codvps/internal/settings"
	"github.com/egginsect/codvps/internal/systemd"
)

// oldClaudeUnitArgv is what claude-remote@.service exec'd before it
// started through internal remote-start.
func oldClaudeUnitArgv(repository string) []string {
	return []string{"claude", "remote-control", "--spawn", "worktree", "--capacity", "4", "--no-create-session-in-dir", "--remote-control-session-name-prefix", "cloud-" + repository}
}

// registerRepos registers a git checkout per name and returns their paths.
func registerRepos(t *testing.T, layout *paths.Layout, names ...string) map[string]string {
	t.Helper()
	dests := map[string]string{}
	var entries []registry.Entry
	for _, name := range names {
		dest := filepath.Join(t.TempDir(), name+" checkout")
		if _, stderr, code, err := runner.NewExecRunner().Run("git", "init", dest); err != nil || code != 0 {
			t.Fatalf("init: %v %s", err, stderr)
		}
		dests[name] = dest
		entries = append(entries, registry.Entry{Name: name, Path: dest})
	}
	if err := registry.WriteEntries(layout.RepositoriesPath(), entries); err != nil {
		t.Fatal(err)
	}
	return dests
}

// installCLI writes an executable script at ~/.local/bin/<name>.
func installCLI(t *testing.T, layout *paths.Layout, name, script string) string {
	t.Helper()
	path := filepath.Join(layout.Home(), ".local", "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

type launch struct {
	path string
	argv []string
	env  []string
	cwd  string
}

func TestInternalRemoteStartExecsTheUnitArgv(t *testing.T) {
	layout := manualLayout(t)
	dests := registerRepos(t, layout, "sample")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := shellExecOptions(t)
	environ := opts.Environ
	opts.Environ = func() []string { return append(environ(), "PATH="+bin+":/usr/bin:/bin") }
	var launches []launch
	opts.Exec = func(path string, argv, env []string) error {
		cwd, _ := os.Getwd()
		launches = append(launches, launch{path, argv, env, cwd})
		return nil
	}

	// The generated unit: ExecStart is codvps internal remote-start.
	var unit []byte
	for _, u := range systemd.ClaudeUserUnits() {
		if u.Name == systemd.ClaudeRemoteTemplate {
			unit = u.Content
		}
	}
	var execStart []string
	for _, line := range strings.Split(string(unit), "\n") {
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			execStart = strings.Fields(strings.ReplaceAll(rest, "%i", "sample"))
		}
	}
	if len(execStart) < 3 || execStart[0] != systemd.CodvpsBinary || execStart[1] != "internal" || execStart[2] != "remote-start" {
		t.Fatalf("claude-remote@.service ExecStart = %q", execStart)
	}
	if err := (&internalCmd{}).Execute(execStart[1:2]); err == nil {
		t.Fatal("internal with no helper must be a usage error")
	}
	if err := internalRemoteStart(execStart[3:], opts); err != nil {
		t.Fatal(err)
	}
	// The unit's former ExecStart, through shell-exec.
	old := append([]string{"--provider", "claude", "--repo", "sample", "--"}, oldClaudeUnitArgv("sample")...)
	if err := internalShellExec(old, providers.All(), opts); err != nil {
		t.Fatal(err)
	}
	if len(launches) != 2 {
		t.Fatalf("launches = %+v", launches)
	}
	got, want := launches[0], launches[1]
	if !reflect.DeepEqual(got.argv, oldClaudeUnitArgv("sample")) {
		t.Errorf("remote-start argv = %q, want %q", got.argv, oldClaudeUnitArgv("sample"))
	}
	if got.path != filepath.Join(bin, "claude") || got.path != want.path {
		t.Errorf("remote-start path = %q, shell-exec path = %q", got.path, want.path)
	}
	if got.cwd != dests["sample"] || got.cwd != want.cwd {
		t.Errorf("remote-start cwd = %q, shell-exec cwd = %q, checkout %q", got.cwd, want.cwd, dests["sample"])
	}
	if !reflect.DeepEqual(got.env, want.env) {
		t.Errorf("remote-start env = %q\nshell-exec env = %q", got.env, want.env)
	}
	for _, e := range got.env {
		for _, gone := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_BASE_URL=", "CLAUDE_CODE_OAUTH_TOKEN="} {
			if strings.HasPrefix(e, gone) {
				t.Errorf("%s was not removed", gone)
			}
		}
	}
	if !slices.Contains(got.env, "FOO=bar") {
		t.Errorf("login-shell environment lost: %q", got.env)
	}
}

func TestInternalRemoteStartCursorKeepsUnitIdentity(t *testing.T) {
	layout := manualLayout(t)
	registerRepos(t, layout, "sample")
	opts, argv, env := shellExecOptions(t)
	if err := internalRemoteStart([]string{"--repo", "sample", "--provider", "cursor"}, opts); err != nil {
		t.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(layout.Home(), ".local", "bin", "agent"), "worker", "--name", host + "-sample", "--worker-dir", ".", "--data-dir", filepath.Join(layout.Home(), ".local", "share", "cursor-agent", "codvps-workers", "sample"), "start"}
	if !reflect.DeepEqual(*argv, want) {
		t.Errorf("argv = %q, want %q", *argv, want)
	}
	if !slices.Contains(*env, "NO_OPEN_BROWSER=1") {
		t.Errorf("env lacks NO_OPEN_BROWSER=1: %q", *env)
	}
}

func TestInternalRemoteStartRejectsBadUsage(t *testing.T) {
	layout := manualLayout(t)
	registerRepos(t, layout, "sample")
	opts, argv, _ := shellExecOptions(t)
	for _, args := range [][]string{nil, {"--provider", "claude"}, {"--provider", "claude", "--repo"}, {"--provider", "claude", "--unset", "sample"}, {"--provider", "claude", "--repo", "sample", "--", "x"}} {
		var usage *InvalidUsageError
		if err := internalRemoteStart(args, opts); !errors.As(err, &usage) {
			t.Errorf("%q: err = %v, want usage error", args, err)
		}
	}
	for _, args := range [][]string{{"--provider", "codex", "--repo", "sample"}, {"--provider", "claude", "--repo", "missing"}} {
		if err := internalRemoteStart(args, opts); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	if *argv != nil {
		t.Errorf("a rejected remote-start launched %q", *argv)
	}
}

func TestInternalUsageListsRemoteStart(t *testing.T) {
	if !slices.Contains(coreInternal, "remote-start") {
		t.Fatalf("coreInternal = %q", coreInternal)
	}
}

// fakeWorkers is a workers whose processes are a map: no real CLI runs.
type fakeWorkers struct {
	*workers
	spawned []session.Plan
	live    map[int]bool
	exits   bool // a new worker exits at once
	stuck   bool // SIGTERM is ignored
	signals []int
	nextPID int
	out     strings.Builder
}

func newFakeWorkers(t *testing.T, layout *paths.Layout) *fakeWorkers {
	t.Helper()
	f := &fakeWorkers{workers: newWorkers(layout), live: map[int]bool{}, nextPID: 4242}
	f.spawn = func(plan session.Plan, log *os.File) (int, <-chan struct{}, error) {
		f.spawned = append(f.spawned, plan)
		pid := f.nextPID
		f.nextPID++
		done := make(chan struct{})
		if f.exits {
			close(done)
		} else {
			f.live[pid] = true
		}
		return pid, done, nil
	}
	f.alive = func(rec workerRecord) bool { return f.live[rec.pid] }
	f.started = func(int) string { return "777" }
	f.terminate = func(pid int) error {
		f.signals = append(f.signals, pid)
		if !f.stuck {
			delete(f.live, pid)
		}
		return nil
	}
	f.after = func(time.Duration) <-chan time.Time {
		if f.exits {
			return nil // never fires: done wins deterministically
		}
		c := make(chan time.Time, 1)
		c <- time.Time{}
		return c
	}
	f.sleep = func(time.Duration) {}
	f.workers.out = &f.out
	return f
}

func manualWorkerHost(t *testing.T, names ...string) (*paths.Layout, map[string]string) {
	t.Helper()
	layout := manualLayout(t)
	dests := registerRepos(t, layout, names...)
	installCLI(t, layout, "claude", "#!/bin/sh\nexit 0\n")
	installCLI(t, layout, "agent", "#!/bin/sh\nexit 0\n")
	return layout, dests
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestBackgroundStartWritesPrivatePidAndLog(t *testing.T) {
	layout, dests := manualWorkerHost(t, "sample")
	f := newFakeWorkers(t, layout)
	if err := f.startOne("claude", "sample"); err != nil {
		t.Fatal(err)
	}
	want, err := session.Start(layout, "claude", "sample", runner.NewExecRunner())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.spawned) != 1 || !reflect.DeepEqual(f.spawned[0], want) || f.spawned[0].Dir != dests["sample"] {
		t.Fatalf("spawned %+v, want session.Start's %+v", f.spawned, want)
	}
	pidPath := filepath.Join(layout.StateDir(), "remote", "claude-sample.pid")
	logPath := filepath.Join(layout.StateDir(), "remote", "claude-sample.log")
	if layout.RemoteDir() != filepath.Dir(pidPath) {
		t.Fatalf("RemoteDir = %q", layout.RemoteDir())
	}
	content, err := os.ReadFile(pidPath)
	if err != nil || string(content) != "4242 777\n" {
		t.Fatalf("pid file %q %v", content, err)
	}
	if m := mode(t, layout.RemoteDir()); m != 0o700 {
		t.Errorf("remote dir mode %o", m)
	}
	for _, p := range []string{pidPath, logPath} {
		if m := mode(t, p); m != 0o600 {
			t.Errorf("%s mode %o", p, m)
		}
	}
	if log, _ := os.ReadFile(logPath); !strings.Contains(string(log), "codvps remote start claude sample") {
		t.Errorf("log = %q", log)
	}
	if !strings.Contains(f.out.String(), "pid 4242") || !strings.Contains(f.out.String(), logPath) {
		t.Errorf("out = %q", f.out.String())
	}

	// Already running: refused, and nothing new is spawned.
	if err := f.startOne("claude", "sample"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start = %v", err)
	}
	if len(f.spawned) != 1 {
		t.Fatalf("respawned a running worker")
	}

	// The process died: the stale pid file is replaced.
	delete(f.live, 4242)
	if err := f.startOne("claude", "sample"); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(pidPath); string(content) != "4243 777\n" {
		t.Fatalf("stale pid file kept: %q", content)
	}
}

func TestBackgroundStartReportsEarlyExit(t *testing.T) {
	layout, _ := manualWorkerHost(t, "sample")
	f := newFakeWorkers(t, layout)
	f.exits = true
	logPath := filepath.Join(layout.RemoteDir(), "claude-sample.log")
	err := f.startOne("claude", "sample")
	if err == nil || !strings.Contains(err.Error(), "exited") || !strings.Contains(err.Error(), logPath) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.RemoteDir(), "claude-sample.pid")); !os.IsNotExist(err) {
		t.Fatal("pid file of an exited worker kept")
	}
}

func TestBackgroundStartAllContinuesPastFailures(t *testing.T) {
	layout, _ := manualWorkerHost(t, "alpha", "beta")
	// A registered repository whose checkout is not a git repository.
	broken := filepath.Join(t.TempDir(), "broken")
	if err := os.Mkdir(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil {
		t.Fatal(err)
	}
	entries = append([]registry.Entry{{Name: "broken", Path: broken}}, entries...)
	if err := registry.WriteEntries(layout.RepositoriesPath(), entries); err != nil {
		t.Fatal(err)
	}
	f := newFakeWorkers(t, layout)
	if err := f.startOne("claude", "beta"); err != nil {
		t.Fatal(err)
	}
	err = f.startAll("claude")
	if err == nil || !strings.Contains(err.Error(), "1 of 3") || !strings.Contains(err.Error(), "broken:") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "alpha") || strings.Contains(err.Error(), "beta") {
		t.Fatalf("healthy repositories reported as failures: %v", err)
	}
	if len(f.spawned) != 2 || !strings.HasSuffix(f.spawned[1].Args[len(f.spawned[1].Args)-1], "cloud-alpha") {
		t.Fatalf("spawned %+v", f.spawned)
	}
	if !strings.Contains(f.out.String(), "beta is already running") {
		t.Fatalf("out = %q", f.out.String())
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(layout.RemoteDir(), "claude-"+name+".pid")); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestBackgroundStopOneAllAndNone(t *testing.T) {
	layout, _ := manualWorkerHost(t, "alpha", "beta")
	f := newFakeWorkers(t, layout)
	if err := f.stop("claude", ""); err != nil || !strings.Contains(f.out.String(), "No background claude workers are running") {
		t.Fatalf("stop none = %v %q", err, f.out.String())
	}
	if err := f.startAll("claude"); err != nil {
		t.Fatal(err)
	}
	if err := f.startOne("cursor", "alpha"); err != nil {
		t.Fatal(err)
	}
	f.out.Reset()
	if err := f.stop("claude", "alpha"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.signals, []int{4242}) || !strings.Contains(f.out.String(), "Stopped claude worker for alpha") {
		t.Fatalf("signals %v out %q", f.signals, f.out.String())
	}
	if _, err := os.Stat(filepath.Join(layout.RemoteDir(), "claude-alpha.pid")); !os.IsNotExist(err) {
		t.Fatal("pid file kept after stop")
	}
	if err := f.stop("claude", ""); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.signals, []int{4242, 4243}) {
		t.Fatalf("stop all signalled %v; the cursor worker must be left alone", f.signals)
	}
	if _, err := os.Stat(filepath.Join(layout.RemoteDir(), "cursor-alpha.pid")); err != nil {
		t.Fatal(err)
	}

	// A worker that ignores SIGTERM fails the stop and keeps its pid file.
	f.stuck = true
	if err := f.stop("cursor", "alpha"); err == nil || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("stuck stop = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.RemoteDir(), "cursor-alpha.pid")); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundStatus(t *testing.T) {
	layout, _ := manualWorkerHost(t, "alpha", "beta")
	f := newFakeWorkers(t, layout)
	if err := f.status("", ""); err != nil || !strings.Contains(f.out.String(), "No background remote workers") {
		t.Fatalf("empty status %v %q", err, f.out.String())
	}
	if err := f.startAll("claude"); err != nil {
		t.Fatal(err)
	}
	delete(f.live, 4243)
	f.out.Reset()
	if err := f.status("claude", ""); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if !strings.Contains(out, "claude alpha: running (pid 4242); log: "+filepath.Join(layout.RemoteDir(), "claude-alpha.log")) ||
		!strings.Contains(out, "claude beta: not running (stale pid 4243)") {
		t.Fatalf("status = %q", out)
	}
	f.out.Reset()
	if err := f.status("cursor", "alpha"); err != nil || f.out.String() != "cursor alpha: not running\n" {
		t.Fatalf("status = %q %v", f.out.String(), err)
	}
}

func TestDetachedCommandIsANewSessionWithThePlan(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	plan := session.Plan{Binary: "/bin/native", Args: []string{"a b"}, Dir: "/checkout", Env: []string{"NO_OPEN_BROWSER=1"}, RemoveEnv: []string{"ANTHROPIC_API_KEY"}}
	cmd := detachedCommand(plan, []string{"ANTHROPIC_API_KEY=x", "KEEP=1", "NO_OPEN_BROWSER=0"}, stdin, log)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatalf("not a new session: %+v", cmd.SysProcAttr)
	}
	if cmd.Path != "/bin/native" || !reflect.DeepEqual(cmd.Args, []string{"/bin/native", "a b"}) || cmd.Dir != "/checkout" {
		t.Fatalf("cmd %q %q %q", cmd.Path, cmd.Args, cmd.Dir)
	}
	if !reflect.DeepEqual(cmd.Env, []string{"KEEP=1", "NO_OPEN_BROWSER=1"}) {
		t.Fatalf("env %q", cmd.Env)
	}
	if cmd.Stdin != stdin || cmd.Stdout != log || cmd.Stderr != log {
		t.Fatal("stdio not wired to /dev/null and the log")
	}
}

// End to end with real processes: a fake claude writes its argv to the log
// and waits; stop ends its process group.
func TestBackgroundWorkerRealProcess(t *testing.T) {
	layout, _ := manualWorkerHost(t, "sample")
	installCLI(t, layout, "claude", "#!/bin/sh\necho \"argv: $*\"\nsleep 30 &\nwait\n")
	w := newWorkers(layout)
	var out strings.Builder
	w.out = &out
	w.settle = 200 * time.Millisecond
	if err := w.startOne("claude", "sample"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(layout.RemoteDir(), "claude-sample.pid"))
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := readWorker(filepath.Join(layout.RemoteDir(), "claude-sample.pid"), "claude", "sample")
	if !ok || !processAlive(rec) {
		t.Fatalf("worker not alive: %q", content)
	}
	if pgid, err := syscall.Getpgid(rec.pid); err != nil || pgid != rec.pid {
		t.Fatalf("worker is not its own process group: %d %v", pgid, err)
	}
	if err := w.stop("claude", "sample"); err != nil {
		t.Fatal(err)
	}
	if processAlive(rec) {
		t.Fatal("worker survived stop")
	}
	if err := syscall.Kill(-rec.pid, 0); err == nil {
		t.Fatal("the worker's process group survived stop")
	}
	log, _ := os.ReadFile(filepath.Join(layout.RemoteDir(), "claude-sample.log"))
	if !strings.Contains(string(log), "argv: "+strings.Join(oldClaudeUnitArgv("sample")[1:], " ")) {
		t.Fatalf("log = %q", log)
	}

	installCLI(t, layout, "claude", "#!/bin/sh\necho boom >&2\nexit 3\n")
	w.settle = 5 * time.Second
	if err := w.startOne("claude", "sample"); err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("early exit = %v", err)
	}
	if log, _ := os.ReadFile(filepath.Join(layout.RemoteDir(), "claude-sample.log")); !strings.Contains(string(log), "boom") {
		t.Fatalf("log = %q", log)
	}
}

func TestProcessStartParsesProcStat(t *testing.T) {
	first := processStart(os.Getpid())
	if first == "" {
		t.Skip("/proc is unavailable")
	}
	if first != processStart(os.Getpid()) {
		t.Fatal("unstable start time")
	}
	if processAlive(workerRecord{pid: os.Getpid(), start: "1"}) {
		t.Fatal("a different start time must not count as the worker")
	}
}

// remoteHost replaces the worker manager behind codvps remote.
func remoteHost(t *testing.T, names ...string) (*paths.Layout, *fakeWorkers) {
	t.Helper()
	layout, _ := manualWorkerHost(t, names...)
	f := newFakeWorkers(t, layout)
	previous := remoteWorkers
	remoteWorkers = func(*paths.Layout) *workers { return f.workers }
	t.Cleanup(func() { remoteWorkers = previous })
	return layout, f
}

func TestRemoteStartWithoutRepoBackgroundsEveryRepository(t *testing.T) {
	_, f := remoteHost(t, "alpha", "beta")
	if err := (&remoteCmd{}).Execute([]string{"start", "claude"}); err != nil {
		t.Fatal(err)
	}
	if len(f.spawned) != 2 {
		t.Fatalf("spawned %d workers", len(f.spawned))
	}
	if err := (&remoteCmd{}).Execute([]string{"start", "cursor", "alpha", "-d"}); err != nil {
		t.Fatal(err)
	}
	if err := (&remoteCmd{}).Execute([]string{"start", "--background", "cursor", "alpha"}); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start = %v", err)
	}
	if err := (&remoteCmd{}).Execute([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); !strings.Contains(out, "claude alpha: running") || !strings.Contains(out, "cursor alpha: running") {
		t.Fatalf("status = %q", out)
	}
	if err := (&remoteCmd{}).Execute([]string{"stop", "claude"}); err != nil {
		t.Fatal(err)
	}
	if len(f.signals) != 2 {
		t.Fatalf("signals = %v", f.signals)
	}
}

func TestRemoteCommandRejectsInvalidForms(t *testing.T) {
	_, f := remoteHost(t, "sample")
	for _, args := range [][]string{
		nil,
		{"start"},
		{"run", "claude", "sample", "-d"},
		{"stop", "claude", "-d"},
		{"start", "claude", "sample", "--detach"},
		{"start", "claude", "sample", "extra"},
	} {
		var usage *InvalidUsageError
		if err := (&remoteCmd{}).Execute(args); !errors.As(err, &usage) {
			t.Errorf("%q: err = %v, want usage error", args, err)
		}
	}
	for _, args := range [][]string{
		{"start", "codex", "sample"},
		{"start", "codex", "sample", "-d"},
		{"stop", "codex", "sample"},
		{"status", "codex", "sample"},
	} {
		if err := (&remoteCmd{}).Execute(args); err == nil || !strings.Contains(err.Error(), "host-wide") {
			t.Errorf("%q: err = %v, want the host-wide refusal", args, err)
		}
	}
	for _, args := range [][]string{{"start", "opencode", "sample"}, {"pair", "claude", "sample"}, {"run", "claude"}} {
		if err := (&remoteCmd{}).Execute(args); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	if len(f.spawned) != 0 {
		t.Fatalf("a rejected form spawned %+v", f.spawned)
	}
}

func TestRemoteCommandRequiresManualMode(t *testing.T) {
	layout := manualLayout(t)
	if err := os.Remove(settings.Path(layout)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := (&remoteCmd{}).Execute([]string{"status"}); err == nil || !strings.Contains(err.Error(), "session_mode manual") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequireSystemdModePointsAtRemoteStart(t *testing.T) {
	manualLayout(t)
	err := requireSystemdMode()
	if err == nil || !strings.Contains(err.Error(), "codvps remote start <claude|cursor|codex> [repo] [--background]") {
		t.Fatalf("err = %v", err)
	}
}

func TestHelpListsManualRemoteForms(t *testing.T) {
	help := helpText()
	for _, form := range []string{
		"  remote start <claude|cursor> [<repo>] [--background|-d]",
		"  remote stop <claude|cursor> [<repo>]",
		"  remote status [<claude|cursor> [<repo>]]",
		"  remote run <claude|cursor> <repo>",
		"  remote <start|stop|pair|status> codex",
	} {
		if !strings.Contains(help, form+"\n") && !strings.Contains(help, form+" ") {
			t.Errorf("help lacks %q", form)
		}
	}
}
