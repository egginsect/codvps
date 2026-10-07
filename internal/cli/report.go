package cli

import (
	"errors"
	"fmt"
	"io"
)

// ReportError writes err to w as "codvps: <err>\n", unless err (or an error
// it wraps) already reported itself to the user. A branch marks its own
// error type as already reported by giving it a Reported() bool method
// that returns true; ReportError finds it by duck typing so callers never
// need to import a shared error type.
func ReportError(w io.Writer, err error) {
	var r interface{ Reported() bool }
	if errors.As(err, &r) && r.Reported() {
		return
	}
	_, _ = fmt.Fprintf(w, "codvps: %s\n", err)
}

// reportedError marks an error that its command already printed to stderr,
// so ReportError skips it. It keeps the wrapped error reachable through
// errors.As and passes through the wrapped error's exit code.
type reportedError struct {
	err error
}

func (e *reportedError) Error() string  { return e.err.Error() }
func (e *reportedError) Unwrap() error  { return e.err }
func (e *reportedError) Reported() bool { return true }

// ExitCode returns the wrapped error's exit code, or 1 when it has none.
func (e *reportedError) ExitCode() int {
	var ec interface{ ExitCode() int }
	if errors.As(e.err, &ec) {
		return ec.ExitCode()
	}
	return 1
}
