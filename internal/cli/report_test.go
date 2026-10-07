package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestReportErrorPlainErrorPrintedOnce(t *testing.T) {
	var buf bytes.Buffer
	ReportError(&buf, errors.New("boom"))

	want := "codvps: boom\n"
	if got := buf.String(); got != want {
		t.Errorf("ReportError() wrote %q, want %q", got, want)
	}
}

func TestReportErrorWrappedReportedErrorNotPrinted(t *testing.T) {
	var buf bytes.Buffer
	wrapped := fmt.Errorf("context: %w", &InvalidUsageError{message: "invalid usage"})
	ReportError(&buf, wrapped)

	if got := buf.String(); got != "" {
		t.Errorf("ReportError() wrote %q, want nothing", got)
	}
}

func TestReportErrorInvalidUsageNotPrinted(t *testing.T) {
	var buf bytes.Buffer
	ReportError(&buf, &InvalidUsageError{message: "invalid usage"})

	if got := buf.String(); got != "" {
		t.Errorf("ReportError() wrote %q, want nothing", got)
	}
}

func TestReportErrorNotImplementedNotPrinted(t *testing.T) {
	var buf bytes.Buffer
	ReportError(&buf, &NotImplementedError{message: "not implemented"})

	if got := buf.String(); got != "" {
		t.Errorf("ReportError() wrote %q, want nothing", got)
	}
}

func TestReportErrorUnknownCommandNotPrinted(t *testing.T) {
	var buf bytes.Buffer
	ReportError(&buf, &unknownCommandError{name: "bogus"})

	if got := buf.String(); got != "" {
		t.Errorf("ReportError() wrote %q, want nothing", got)
	}
}

type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return "blocked" }
func (e *exitCodeError) ExitCode() int { return e.code }

func TestReportedErrorIsSkippedAndKeepsExitCode(t *testing.T) {
	inner := &exitCodeError{code: 2}
	err := error(&reportedError{err: inner})

	var buf bytes.Buffer
	ReportError(&buf, err)
	if got := buf.String(); got != "" {
		t.Errorf("ReportError() wrote %q, want nothing", got)
	}
	ec, ok := err.(interface{ ExitCode() int })
	if !ok || ec.ExitCode() != 2 {
		t.Errorf("ExitCode() not passed through: ok=%v", ok)
	}
	var target *exitCodeError
	if !errors.As(err, &target) {
		t.Error("errors.As cannot reach the wrapped error")
	}
	if code := (&reportedError{err: errors.New("plain")}).ExitCode(); code != 1 {
		t.Errorf("ExitCode() = %d for a plain error, want 1", code)
	}
}

func TestLoginGitHubBadFlagIsReportedOnceWithExitCode2(t *testing.T) {
	err := handleLoginGitHub([]string{"--no-such-flag"})
	var buf bytes.Buffer
	ReportError(&buf, err)
	if got := buf.String(); got != "" {
		t.Errorf("flag error printed twice: %q", got)
	}
	ec, ok := err.(interface{ ExitCode() int })
	if !ok || ec.ExitCode() != 2 {
		t.Errorf("bad flag exit code: ok=%v", ok)
	}
}
