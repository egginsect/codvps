package runner

import (
	"bytes"
	"testing"
)

func TestExecRunnerSuccess(t *testing.T) {
	runner := NewExecRunner()
	stdout, _, exitCode, err := runner.Run("echo", "hello")

	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if stdout != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout, "hello\n")
	}
}

func TestExecRunnerFailure(t *testing.T) {
	runner := NewExecRunner()
	_, _, exitCode, err := runner.Run("false")

	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
	if err == nil {
		t.Errorf("err = nil, want non-nil")
	}
}

func TestExecRunnerNotFound(t *testing.T) {
	runner := NewExecRunner()
	_, _, exitCode, err := runner.Run("this-command-does-not-exist-xyz")

	if exitCode != 1 {
		t.Errorf("exitCode = %d, want 1", exitCode)
	}
	if err == nil {
		t.Errorf("err = nil, want non-nil")
	}
}

func TestFakeRunnerSetResponse(t *testing.T) {
	fake := NewFakeRunner()
	fake.SetResponse("test", []string{"arg"}, Response{
		Stdout:   "output",
		Stderr:   "error",
		ExitCode: 42,
	})

	stdout, stderr, exitCode, err := fake.Run("test", "arg")

	if exitCode != 42 {
		t.Errorf("exitCode = %d, want 42", exitCode)
	}
	if stdout != "output" {
		t.Errorf("stdout = %q, want %q", stdout, "output")
	}
	if stderr != "error" {
		t.Errorf("stderr = %q, want %q", stderr, "error")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestFakeRunnerDefault(t *testing.T) {
	fake := NewFakeRunner()
	stdout, _, exitCode, err := fake.Run("unknown", "command")

	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	fake := NewFakeRunner()
	_, _, _, _ = fake.Run("test", "arg1")
	_, _, _, _ = fake.Run("test2", "arg2", "arg3")

	if len(fake.GetCalls()) != 2 {
		t.Errorf("len(calls) = %d, want 2", len(fake.GetCalls()))
	}

	if fake.GetCalls()[0].Name != "test" || len(fake.GetCalls()[0].Args) != 1 {
		t.Errorf("first call mismatch")
	}

	if fake.GetCalls()[1].Name != "test2" || len(fake.GetCalls()[1].Args) != 2 {
		t.Errorf("second call mismatch")
	}
}

func TestFakeRunnerReset(t *testing.T) {
	fake := NewFakeRunner()
	_, _, _, _ = fake.Run("test")
	if len(fake.GetCalls()) != 1 {
		t.Errorf("expected 1 call before reset")
	}

	fake.Reset()
	if len(fake.GetCalls()) != 0 {
		t.Errorf("expected 0 calls after reset")
	}
}

func TestExecRunnerWithIO(t *testing.T) {
	runner := NewExecRunner()

	stdin := bytes.NewReader([]byte("input"))
	var stdout, stderr bytes.Buffer

	exitCode, err := runner.RunWithIO("cat", []string{}, stdin, &stdout, &stderr)

	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if stdout.String() != "input" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "input")
	}
}

func TestFakeRunnerWithIO(t *testing.T) {
	fake := NewFakeRunner()
	fake.SetResponse("test", []string{}, Response{
		Stdout:   "test output",
		Stderr:   "test error",
		ExitCode: 0,
	})

	var stdout, stderr bytes.Buffer
	exitCode, err := fake.RunWithIO("test", []string{}, nil, &stdout, &stderr)

	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
	if stdout.String() != "test output" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "test output")
	}
	if stderr.String() != "test error" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "test error")
	}
}
