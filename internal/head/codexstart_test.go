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

	"github.com/egginsect/codvps/internal/runner"
)

// scriptedCodex answers successive `remote-control start` calls from a
// script and records every call.
type scriptedCodex struct {
	starts []runner.Response
	stop   runner.Response
	calls  []string
}

func (s *scriptedCodex) Run(name string, args ...string) (string, string, int, error) {
	s.calls = append(s.calls, filepath.Base(name)+" "+strings.Join(args, " "))
	switch strings.Join(args, " ") {
	case "remote-control start --json":
		if len(s.starts) == 0 {
			return "", "", 99, fmt.Errorf("unexpected extra start")
		}
		r := s.starts[0]
		s.starts = s.starts[1:]
		return r.Stdout, r.Stderr, r.ExitCode, r.Err
	case "remote-control stop --json":
		return s.stop.Stdout, s.stop.Stderr, s.stop.ExitCode, s.stop.Err
	}
	return "", "", 98, fmt.Errorf("unexpected call %v", args)
}

func (s *scriptedCodex) RunWithIO(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 98, errors.New("unexpected RunWithIO")
}

const erroredRelay = "Error: Remote control is enabled on vps-test but the connection is errored.\n"

func startEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".codex", "packages", "standalone", "current", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil { //nolint:gosec // fixture must be executable; never run
		t.Fatal(err)
	}
	return map[string]string{"HOME": home, "CODEX_HOME": filepath.Join(home, ".codex")}, bin
}

func runStart(env map[string]string, codex *scriptedCodex) (int, string, string, error) {
	var out, diag bytes.Buffer
	code, err := RunRemoteStart(RemoteStartOptions{
		Getenv:   func(k string) string { return env[k] },
		Runner:   codex,
		Binary:   "codex",
		Hostname: func() (string, error) { return "vps-test", nil },
		Out:      &out,
		Diag:     &diag,
	})
	return code, out.String(), diag.String(), err
}

func failed(stderr string, code int) runner.Response {
	return runner.Response{Stderr: stderr, ExitCode: code, Err: fmt.Errorf("exit %d", code)}
}

// A clean start is one start.
func TestRemoteStart_StartsOnce(t *testing.T) {
	env, _ := startEnv(t)
	codex := &scriptedCodex{starts: []runner.Response{{Stdout: "{\"status\":\"running\",\"pid\":7}\n"}}}
	code, out, _, err := runStart(env, codex)
	if code != 0 || err != nil || !strings.Contains(out, `"status":"running"`) || len(codex.calls) != 1 {
		t.Fatalf("code=%d err=%v out=%q calls=%v", code, err, out, codex.calls)
	}
}

// Reference scenario test_codex_start_recovers_one_transient_errored_connection:
// exactly Codex's errored-relay line triggers one stop and one more start;
// the retry's success is the unit's success, and the journal says why.
func TestRemoteStart_RecoversOneTransientErroredConnection(t *testing.T) {
	env, _ := startEnv(t)
	codex := &scriptedCodex{starts: []runner.Response{failed(erroredRelay, 1), {Stdout: "{\"status\":\"running\"}\n"}}}
	code, out, diag, err := runStart(env, codex)
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if got := strings.Join(codex.calls, "|"); got != "codex remote-control start --json|codex remote-control stop --json|codex remote-control start --json" {
		t.Fatalf("calls = %s", got)
	}
	if !strings.Contains(out, strings.TrimSpace(erroredRelay)) || !strings.Contains(diag, "stopping and retrying once") {
		t.Fatalf("out=%q diag=%q", out, diag)
	}
}

// A persistent errored relay is not masked: two starts, and the retry's
// status fails the unit. A failing stop before the retry is noted and the
// retry still happens.
func TestRemoteStart_PersistentErrorStillFails(t *testing.T) {
	env, _ := startEnv(t)
	codex := &scriptedCodex{starts: []runner.Response{failed(erroredRelay, 1), failed(erroredRelay, 4)}, stop: failed("stop failed\n", 3)}
	code, _, diag, err := runStart(env, codex)
	if code != 4 || err == nil {
		t.Fatalf("code=%d err=%v, want the retry's status 4", code, err)
	}
	if n := strings.Count(strings.Join(codex.calls, "|"), "start --json"); n != 2 {
		t.Fatalf("%d starts, want exactly 2", n)
	}
	if !strings.Contains(diag, "Codex Remote stop before the bounded retry exited with status 3; retrying once anyway.") {
		t.Fatalf("diag = %q", diag)
	}
}

// Any other failure -- including the errored-relay text for another host
// -- is returned as is, without a retry.
func TestRemoteStart_OtherFailuresAreNotRetried(t *testing.T) {
	for _, stderr := range []string{"Error: not logged in\n", "Error: Remote control is enabled on other-host but the connection is errored.\n"} {
		env, _ := startEnv(t)
		codex := &scriptedCodex{starts: []runner.Response{failed(stderr, 2)}}
		code, _, _, err := runStart(env, codex)
		if code != 2 || err == nil || len(codex.calls) != 1 {
			t.Fatalf("%q: code=%d err=%v calls=%v", stderr, code, err, codex.calls)
		}
	}
}

// The executable is the standalone install under CODEX_HOME, never an
// inherited pin; a missing one fails with its path.
func TestRemoteStart_ExecutableSelection(t *testing.T) {
	env, bin := startEnv(t)
	pinned := filepath.Join(t.TempDir(), "codex-1.2.3")
	if err := os.WriteFile(pinned, []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil { //nolint:gosec // fixture must be executable; never run
		t.Fatal(err)
	}
	var ran []string
	rec := recordingRunner{ran: &ran}
	var out, diag bytes.Buffer
	opts := RemoteStartOptions{Getenv: func(k string) string { return env[k] }, Runner: rec, Binary: "codex", Hostname: os.Hostname, Out: &out, Diag: &diag}
	if code, err := RunRemoteStart(opts); code != 0 || err != nil || ran[0] != bin {
		t.Fatalf("standalone: code=%d err=%v ran=%v", code, err, ran)
	}
	env["CODVPS_CODEX_BIN"] = pinned
	if code, err := RunRemoteStart(opts); code != 0 || err != nil || ran[1] != bin {
		t.Fatalf("a leftover pin was honoured: code=%d err=%v ran=%v", code, err, ran)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	code, err := RunRemoteStart(opts)
	if code != 1 || err == nil || err.Error() != "Codex Remote executable is missing or not executable: "+bin {
		t.Fatalf("missing: code=%d err=%v", code, err)
	}
}

type recordingRunner struct{ ran *[]string }

func (r recordingRunner) Run(name string, _ ...string) (string, string, int, error) {
	*r.ran = append(*r.ran, name)
	return "", "", 0, nil
}

func (r recordingRunner) RunWithIO(string, []string, io.Reader, io.Writer, io.Writer) (int, error) {
	return 98, errors.New("unexpected RunWithIO")
}
