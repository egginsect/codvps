// Package runner provides subprocess execution abstraction.
package runner

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// Runner represents a subprocess executor.
type Runner interface {
	// Run executes a command with arguments, returning stdout, stderr, and error.
	// If error is nil, the command exited with code 0.
	// If error is non-nil, the command exited with non-zero code.
	Run(name string, args ...string) (stdout, stderr string, exitCode int, err error)

	// RunWithIO executes a command with custom stdin/stdout/stderr.
	RunWithIO(name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) (exitCode int, err error)
}

// ExecRunner implements Runner using os/exec.
type ExecRunner struct {
	env []string
}

// NewExecRunner creates a new ExecRunner with optional environment variables.
func NewExecRunner(env ...string) *ExecRunner {
	return &ExecRunner{env: env}
}

// Run executes a command and captures its output.
func (r *ExecRunner) Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	cmd := exec.Command(name, args...)
	if r.env != nil {
		cmd.Env = append(os.Environ(), r.env...)
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err = cmd.Run()
	exitCode = getExitCode(err)

	return outBuf.String(), errBuf.String(), exitCode, err
}

// RunWithIO executes a command with provided stdin/stdout/stderr.
func (r *ExecRunner) RunWithIO(name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) (exitCode int, err error) {
	cmd := exec.Command(name, args...)
	if r.env != nil {
		cmd.Env = append(os.Environ(), r.env...)
	}

	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	exitCode = getExitCode(err)
	return exitCode, err
}

// getExitCode extracts exit code from error, or 0 if no error.
func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	// Default to 1 for non-ExitError (command not found, etc.)
	return 1
}

// FakeRunner records calls and provides canned responses for testing.
type FakeRunner struct {
	Calls     []Call
	Responses map[string]Response
}

// Call represents a recorded command call.
type Call struct {
	Name string
	Args []string
}

// Response represents a canned response for a command.
type Response struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// NewFakeRunner creates a new FakeRunner for testing.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{
		Responses: make(map[string]Response),
	}
}

// Run executes using recorded responses.
func (f *FakeRunner) Run(name string, args ...string) (stdout, stderr string, exitCode int, err error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})

	key := fmt.Sprintf("%s %v", name, args)
	if resp, ok := f.Responses[key]; ok {
		return resp.Stdout, resp.Stderr, resp.ExitCode, resp.Err
	}

	// Default: success
	return "", "", 0, nil
}

// RunWithIO executes using recorded responses with custom IO.
func (f *FakeRunner) RunWithIO(name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) (exitCode int, err error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: args})

	key := fmt.Sprintf("%s %v", name, args)
	if resp, ok := f.Responses[key]; ok {
		if stdout != nil {
			_, _ = io.WriteString(stdout, resp.Stdout)
		}
		if stderr != nil {
			_, _ = io.WriteString(stderr, resp.Stderr)
		}
		return resp.ExitCode, resp.Err
	}

	return 0, nil
}

// SetResponse sets a canned response for a command.
func (f *FakeRunner) SetResponse(name string, args []string, resp Response) {
	key := fmt.Sprintf("%s %v", name, args)
	f.Responses[key] = resp
}

// GetCalls returns all recorded calls.
func (f *FakeRunner) GetCalls() []Call {
	return f.Calls
}

// Reset clears recorded calls.
func (f *FakeRunner) Reset() {
	f.Calls = nil
}
