package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/session"
)

// workerProviders are the providers manual mode runs per-repository
// background workers for, in report order.
var workerProviders = []string{"claude", "cursor"}

// workers manages manual mode's detached remote workers: one process
// group per provider and repository, started from session.Start's plan,
// with a pid file and an append-only log under layout.RemoteDir(). It is
// not a supervisor: nothing restarts a worker that exits.
type workers struct {
	layout *paths.Layout
	// plan builds the worker's session.Start plan.
	plan func(provider, repository string) (session.Plan, error)
	// spawn starts the plan detached with stdout and stderr on log; done is
	// closed when the process exits.
	spawn func(plan session.Plan, log *os.File) (pid int, done <-chan struct{}, err error)
	// alive reports whether a recorded worker is still that process.
	alive func(rec workerRecord) bool
	// started identifies a process instance, for the pid file.
	started func(pid int) string
	// terminate signals a worker's process group.
	terminate func(pid int) error
	// after and sleep are the clock: settle is how long a new worker must
	// survive, stopWait how long stop waits for one to exit.
	after    func(time.Duration) <-chan time.Time
	sleep    func(time.Duration)
	settle   time.Duration
	stopWait time.Duration
	out      io.Writer
}

// workerRecord is one pid file: the worker's pid and process start time.
type workerRecord struct {
	provider, repository string
	pid                  int
	start                string
}

const stopPoll = 100 * time.Millisecond

func newWorkers(layout *paths.Layout) *workers {
	return &workers{
		layout: layout,
		plan: func(provider, repository string) (session.Plan, error) {
			git, err := sessionGit(layout)
			if err != nil {
				return session.Plan{}, err
			}
			return session.Start(layout, provider, repository, git)
		},
		spawn:     spawnDetached,
		alive:     processAlive,
		started:   processStart,
		terminate: func(pid int) error { return syscall.Kill(-pid, syscall.SIGTERM) },
		after:     time.After,
		sleep:     time.Sleep,
		settle:    time.Second,
		stopWait:  10 * time.Second,
		out:       os.Stdout,
	}
}

func (w *workers) pidPath(provider, repository string) string {
	return filepath.Join(w.layout.RemoteDir(), provider+"-"+repository+".pid")
}

func (w *workers) logPath(provider, repository string) string {
	return filepath.Join(w.layout.RemoteDir(), provider+"-"+repository+".log")
}

// startOne starts one background worker; one already running is an error.
func (w *workers) startOne(provider, repository string) error {
	rec, already, err := w.start(provider, repository)
	if err != nil {
		return err
	}
	if already {
		return fmt.Errorf("%s worker for %s is already running (pid %d); log: %s", provider, repository, rec.pid, w.logPath(provider, repository))
	}
	return nil
}

// startAll starts one background worker per registered repository. A
// running worker is reported, not restarted; a failing repository does not
// stop the others, and every failure is named in the returned error.
func (w *workers) startAll(provider string) error {
	entries, err := registry.ReadEntries(w.layout.RepositoriesPath())
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no repositories are registered; add one with codvps repo add <url> or codvps repo add-local <path>")
	}
	var failures []string
	for _, entry := range entries {
		rec, already, err := w.start(provider, entry.Name)
		switch {
		case err != nil:
			failures = append(failures, entry.Name+": "+err.Error())
		case already:
			_, _ = fmt.Fprintf(w.out, "%s worker for %s is already running (pid %d); log: %s\n", provider, entry.Name, rec.pid, w.logPath(provider, entry.Name))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d of %d %s workers failed to start:\n  %s", len(failures), len(entries), provider, strings.Join(failures, "\n  "))
	}
	return nil
}

// start spawns a detached worker unless its recorded process is alive, in
// which case already is true. A stale pid file is removed first. A worker
// that exits within the settle time is reported with its log.
func (w *workers) start(provider, repository string) (workerRecord, bool, error) {
	if err := registry.ValidateName(repository); err != nil {
		return workerRecord{}, false, err
	}
	if err := fsutil.EnsurePrivateDir(w.layout.RemoteDir()); err != nil {
		return workerRecord{}, false, err
	}
	pidPath, logPath := w.pidPath(provider, repository), w.logPath(provider, repository)
	if rec, ok := readWorker(pidPath, provider, repository); ok {
		if w.alive(rec) {
			return rec, true, nil
		}
		if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return rec, false, err
		}
	}
	plan, err := w.plan(provider, repository)
	if err != nil {
		return workerRecord{}, false, err
	}
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return workerRecord{}, false, err
	}
	if err := log.Chmod(0o600); err != nil {
		_ = log.Close()
		return workerRecord{}, false, err
	}
	_, _ = fmt.Fprintf(log, "--- codvps remote start %s %s at %s\n", provider, repository, time.Now().UTC().Format(time.RFC3339))
	pid, done, err := w.spawn(plan, log)
	_ = log.Close()
	if err != nil {
		return workerRecord{}, false, fmt.Errorf("cannot start %s worker for %s: %w (log: %s)", provider, repository, err, logPath)
	}
	rec := workerRecord{provider: provider, repository: repository, pid: pid, start: w.started(pid)}
	if err := fsutil.AtomicWrite(pidPath, []byte(strconv.Itoa(pid)+" "+rec.start+"\n"), 0o600); err != nil {
		return rec, false, err
	}
	select {
	case <-done:
		_ = os.Remove(pidPath)
		return rec, false, fmt.Errorf("%s worker for %s exited right after starting; see %s", provider, repository, logPath)
	case <-w.after(w.settle):
	}
	_, _ = fmt.Fprintf(w.out, "Started %s worker for %s in the background (pid %d); log: %s\n", provider, repository, pid, logPath)
	return rec, false, nil
}

// stop ends one provider's background workers: one repository's, or every
// recorded one. Nothing running is not an error.
func (w *workers) stop(provider, repository string) error {
	recs, err := w.records(provider, repository)
	if err != nil {
		return err
	}
	var failures []string
	stopped := 0
	for _, rec := range recs {
		pidPath := w.pidPath(rec.provider, rec.repository)
		if !w.alive(rec) {
			_ = os.Remove(pidPath)
			continue
		}
		if err := w.terminate(rec.pid); err != nil && !errors.Is(err, syscall.ESRCH) {
			failures = append(failures, fmt.Sprintf("%s: %v", rec.repository, err))
			continue
		}
		exited := false
		for waited := time.Duration(0); ; waited += stopPoll {
			if exited = !w.alive(rec); exited || waited >= w.stopWait {
				break
			}
			w.sleep(stopPoll)
		}
		if !exited {
			failures = append(failures, fmt.Sprintf("%s: pid %d did not exit within %s", rec.repository, rec.pid, w.stopWait))
			continue
		}
		_ = os.Remove(pidPath)
		stopped++
		_, _ = fmt.Fprintf(w.out, "Stopped %s worker for %s (pid %d)\n", rec.provider, rec.repository, rec.pid)
	}
	if len(failures) > 0 {
		return fmt.Errorf("could not stop %s workers:\n  %s", provider, strings.Join(failures, "\n  "))
	}
	if stopped == 0 {
		if repository != "" {
			_, _ = fmt.Fprintf(w.out, "No background %s worker is running for %s.\n", provider, repository)
		} else {
			_, _ = fmt.Fprintf(w.out, "No background %s workers are running.\n", provider)
		}
	}
	return nil
}

// status reports recorded background workers: whether each process is
// alive, and its log. It changes nothing.
func (w *workers) status(provider, repository string) error {
	recs, err := w.records(provider, repository)
	if err != nil {
		return err
	}
	if len(recs) == 0 {
		switch {
		case repository != "":
			_, _ = fmt.Fprintf(w.out, "%s %s: not running\n", provider, repository)
		case provider != "":
			_, _ = fmt.Fprintf(w.out, "No background %s workers are recorded.\n", provider)
		default:
			_, _ = fmt.Fprintln(w.out, "No background remote workers are recorded. Use codvps remote status codex for the Codex daemon.")
		}
		return nil
	}
	for _, rec := range recs {
		state := fmt.Sprintf("running (pid %d)", rec.pid)
		if !w.alive(rec) {
			state = fmt.Sprintf("not running (stale pid %d)", rec.pid)
		}
		_, _ = fmt.Fprintf(w.out, "%s %s: %s; log: %s\n", rec.provider, rec.repository, state, w.logPath(rec.provider, rec.repository))
	}
	return nil
}

// records are the pid files for one provider (every worker provider when
// empty), narrowed to one repository when given.
func (w *workers) records(provider, repository string) ([]workerRecord, error) {
	if repository != "" {
		if err := registry.ValidateName(repository); err != nil {
			return nil, err
		}
		if rec, ok := readWorker(w.pidPath(provider, repository), provider, repository); ok {
			return []workerRecord{rec}, nil
		}
		return nil, nil
	}
	names := workerProviders
	if provider != "" {
		names = []string{provider}
	}
	var recs []workerRecord
	for _, name := range names {
		matches, err := filepath.Glob(filepath.Join(w.layout.RemoteDir(), name+"-*.pid"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, path := range matches {
			repo := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), name+"-"), ".pid")
			if registry.ValidateName(repo) != nil {
				continue
			}
			if rec, ok := readWorker(path, name, repo); ok {
				recs = append(recs, rec)
			}
		}
	}
	return recs, nil
}

// readWorker parses a pid file; a missing or malformed one is no record.
func readWorker(path, provider, repository string) (workerRecord, bool) {
	content, err := os.ReadFile(path)
	if err != nil {
		return workerRecord{}, false
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 || len(fields) > 2 {
		return workerRecord{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 1 {
		return workerRecord{}, false
	}
	rec := workerRecord{provider: provider, repository: repository, pid: pid}
	if len(fields) == 2 {
		rec.start = fields[1]
	}
	return rec, true
}

// detachedCommand runs plan as a new session (its own process group, no
// controlling terminal) with stdin from stdin and output appended to log.
// Binary, arguments, directory and environment are the plan's, exactly as
// session.Execute would exec them.
func detachedCommand(plan session.Plan, environment []string, stdin, log *os.File) *exec.Cmd {
	cmd := exec.Command(plan.Binary, plan.Args...)
	cmd.Dir = plan.Dir
	cmd.Env = session.Environ(plan, environment)
	cmd.Stdin = stdin
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func spawnDetached(plan session.Plan, log *os.File) (int, <-chan struct{}, error) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = devnull.Close() }()
	cmd := detachedCommand(plan, os.Environ(), devnull, log)
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return cmd.Process.Pid, done, nil
}

// processAlive is true while rec's pid still leads its own process group
// (as a worker started with Setsid does) and, when recorded, has the same
// start time, so a reused pid is never mistaken for the worker.
func processAlive(rec workerRecord) bool {
	if err := syscall.Kill(rec.pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	if pgid, err := syscall.Getpgid(rec.pid); err != nil || pgid != rec.pid {
		return false
	}
	if rec.start != "" {
		if now := processStart(rec.pid); now != "" && now != rec.start {
			return false
		}
	}
	return true
}

// processStart is the process's start time in clock ticks since boot
// (/proc/<pid>/stat field 22), or "" when it cannot be read.
func processStart(pid int) string {
	content, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return ""
	}
	// The command name (field 2) may contain spaces; fields resume after
	// its closing parenthesis, at field 3.
	end := strings.LastIndexByte(string(content), ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(content[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
