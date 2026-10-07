package head

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/egginsect/codvps/internal/runner"
)

// RemoteStartOptions are the dependencies of RunRemoteStart, the
// codex-remote@.service ExecStart step.
type RemoteStartOptions struct {
	// Getenv reads the unit's environment (HOME, and CODEX_HOME when set).
	Getenv func(string) string
	// Runner runs the Codex executable.
	Runner runner.Runner
	// Binary is the Codex CLI's executable name, found in the standalone
	// install.
	Binary string
	// Hostname names this host as Codex does in its errored-relay message.
	Hostname func() (string, error)
	// Out receives Codex's own output (the unit's journal).
	Out io.Writer
	// Diag receives this step's own notes (the unit's journal).
	Diag io.Writer
}

// RunRemoteStart starts Codex Remote Control (`codex remote-control start
// --json`) with one bounded retry, and returns the exit status the unit's
// ExecStart must exit with.
//
// Codex can transiently keep an enabled-but-errored relay state after its
// managed daemon was stopped; a second ordinary stop/start cycle repairs it.
// So when, and only when, the first start fails with exactly Codex's
// "Remote control is enabled on <host> but the connection is errored."
// line, this stops Codex once and starts it once more, and returns that
// retry's status unchanged. Any other failure, and a persistent relay,
// network or authentication failure, still fails the unit. Keeping the
// retry here, at the process boundary, gives every systemd start path the
// same single retry.
func RunRemoteStart(opts RemoteStartOptions) (int, error) {
	if opts.Getenv == nil || opts.Runner == nil || opts.Hostname == nil || opts.Out == nil || opts.Diag == nil || opts.Binary == "" {
		return 1, errors.New("codex remote start: Getenv, Runner, Binary, Hostname, Out and Diag are required")
	}
	bin, err := remoteCodexBinary(opts.Getenv, opts.Binary)
	if err != nil {
		return 1, err
	}

	output, status := runCodexCombined(opts, bin, "start")
	if status == 0 {
		return 0, nil
	}
	host, err := opts.Hostname()
	if err != nil {
		return status, fmt.Errorf("codex remote-control start failed (exit %d), and the host name needed to recognise a transient relay error could not be read: %w", status, err)
	}
	expected := "Error: Remote control is enabled on " + host + " but the connection is errored."
	if !hasExactLine(output, expected) {
		return status, fmt.Errorf("codex remote-control start --json exited with status %d", status)
	}

	writeLine(opts.Diag, "Codex Remote connection is transiently errored; stopping and retrying once.")
	if _, stopStatus := runCodexCombined(opts, bin, "stop"); stopStatus != 0 {
		writeLine(opts.Diag, fmt.Sprintf("Codex Remote stop before the bounded retry exited with status %d; retrying once anyway.", stopStatus))
	}
	if _, status = runCodexCombined(opts, bin, "start"); status != 0 {
		return status, fmt.Errorf("codex remote-control start --json exited with status %d after one retry", status)
	}
	return 0, nil
}

// remoteCodexBinary is the Codex the unit runs: the vendor standalone
// install's own "current" link under CODEX_HOME (default ~/.codex), which
// Codex keeps on its latest release.
func remoteCodexBinary(getenv func(string) string, binary string) (string, error) {
	home := getenv("CODEX_HOME")
	if home == "" {
		if h := getenv("HOME"); h != "" {
			home = filepath.Join(h, CodexRemoteDir)
		}
	}
	if home == "" {
		return "", errors.New("neither CODEX_HOME nor HOME is set, so the Codex Remote executable cannot be located")
	}
	bin := CodexStandalone(home, binary)
	fi, err := os.Stat(bin)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("Codex Remote executable is missing or not executable: %s", bin)
	}
	return bin, nil
}

// runCodexCombined runs `<bin> remote-control <verb> --json`, copies its
// output to Out and returns that output with the exit status (1 when the
// process could not run at all, with the reason included in the output).
func runCodexCombined(opts RemoteStartOptions, bin, verb string) (string, int) {
	stdout, stderr, code, err := opts.Runner.Run(bin, "remote-control", verb, "--json")
	output := stdout + stderr
	if err != nil {
		if code == 0 {
			code = 1
		}
		if strings.TrimSpace(output) == "" {
			output = err.Error() + "\n"
		}
	}
	if trimmed := strings.TrimRight(output, "\n"); trimmed != "" {
		writeLine(opts.Out, trimmed)
	}
	return output, code
}

func hasExactLine(output, want string) bool {
	for _, line := range strings.Split(output, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// writeLine writes to the unit's journal streams. A failed write there has
// nowhere else to go, and the exit status still reaches systemd.
func writeLine(w io.Writer, line string) {
	_, _ = fmt.Fprintln(w, line)
}
