package providers

import (
	"io"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

// fakeCursorRunner simulates agent status responses for testing.
type fakeCursorRunner struct {
	authenticated bool
}

func (f *fakeCursorRunner) Run(name string, args ...string) (string, string, int, error) {
	if len(args) >= 1 && args[0] == "status" {
		// agent status --format json returns exit 0 always with JSON status
		if len(args) >= 3 && args[1] == "--format" && args[2] == "json" {
			if f.authenticated {
				return `{"isAuthenticated":true}`, "", 0, nil
			}
			return `{"isAuthenticated":false}`, "", 0, nil
		}
		// Legacy: bare status for older behavior
		if f.authenticated {
			return "", "", 0, nil
		}
		return "", "", 1, nil
	}
	return "", "unexpected command", 127, nil
}

func (f *fakeCursorRunner) RunWithIO(name string, args []string, _ io.Reader, _, _ io.Writer) (int, error) {
	_, _, code, _ := f.Run(name, args...)
	return code, nil
}

// TestCursorLoginAfterVerification tests that Cursor login's After function
// correctly verifies agent status JSON through a testable runner.
func TestCursorLoginAfterVerification(t *testing.T) {
	tests := []struct {
		name          string
		authenticated bool
		wantErr       bool
	}{
		{"logged in", true, false},
		{"logged out", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore the original factory
			originalFactory := cursorRunnerFactory
			defer func() { cursorRunnerFactory = originalFactory }()

			// Inject fake runner with JSON response
			fakeRunner := &fakeCursorRunner{authenticated: tt.authenticated}
			cursorRunnerFactory = func() runner.Runner { return fakeRunner }

			// Get the cursor provider and its login After function
			p := cursor()
			afterFunc := p.Login.Command.After

			// Test the After function
			err := afterFunc("/home/user")
			if (err != nil) != tt.wantErr {
				t.Errorf("After() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
