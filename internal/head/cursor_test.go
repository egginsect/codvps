package head

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/paths"
)

// createTestLayout creates a test Layout for a fake home directory.
func createTestLayout() *paths.Layout {
	layout, err := paths.NewForHome("/home/test", "codvps")
	if err != nil {
		panic(err)
	}
	return layout
}

// fakeRunner implements runner.Runner for testing.
type fakeRunner struct {
	runFunc func(name string, args ...string) (string, string, int, error)
}

func (f *fakeRunner) Run(name string, args ...string) (string, string, int, error) {
	if f.runFunc != nil {
		return f.runFunc(name, args...)
	}
	return "", "", 0, nil
}

func (f *fakeRunner) RunWithIO(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	s, _, code, err := f.Run(name, args...)
	if s != "" {
		if _, err := io.WriteString(stdout, s); err != nil {
			return 0, err
		}
	}
	return code, err
}

// TestNewCursor_ValidOptions succeeds with all dependencies.
func TestNewCursor_ValidOptions(t *testing.T) {
	layout, err := paths.NewForHome("/home/test", "codvps")
	if err != nil {
		t.Fatalf("Failed to create Layout: %v", err)
	}
	opts := CursorOptions{
		Layout:  layout,
		Git:     &fakeRunner{},
		Systemd: &fakeRunner{},
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	c, err := NewCursor(opts)
	if err != nil {
		t.Fatalf("NewCursor with valid opts failed: %v", err)
	}
	if c == nil {
		t.Fatal("NewCursor returned nil cursor")
	}
}

// TestNewCursor_MissingLayout returns error when Layout is nil.
func TestNewCursor_MissingLayout(t *testing.T) {
	opts := CursorOptions{
		Layout:  nil,
		Git:     &fakeRunner{},
		Systemd: &fakeRunner{},
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	_, err := NewCursor(opts)
	if err == nil {
		t.Fatal("NewCursor without Layout should fail")
	}
	if !strings.Contains(err.Error(), "Layout") {
		t.Errorf("error message should mention Layout: %v", err)
	}
}

// TestNewCursor_MissingGit returns error when Git is nil.
func TestNewCursor_MissingGit(t *testing.T) {
	layout, _ := paths.NewForHome("/home/test", "codvps")
	opts := CursorOptions{
		Layout:  layout,
		Git:     nil,
		Systemd: &fakeRunner{},
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	_, err := NewCursor(opts)
	if err == nil {
		t.Fatal("NewCursor without Git should fail")
	}
	if !strings.Contains(err.Error(), "Git") {
		t.Errorf("error message should mention Git: %v", err)
	}
}

// TestNewCursor_MissingSystemd returns error when Systemd is nil.
func TestNewCursor_MissingSystemd(t *testing.T) {
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: nil,
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	_, err := NewCursor(opts)
	if err == nil {
		t.Fatal("NewCursor without Systemd should fail")
	}
	if !strings.Contains(err.Error(), "Systemd") {
		t.Errorf("error message should mention Systemd: %v", err)
	}
}

// TestNewCursor_MissingDiag returns error when Diag is nil.
func TestNewCursor_MissingDiag(t *testing.T) {
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: &fakeRunner{},
		Diag:    nil,
		Sleep:   func(time.Duration) {},
	}
	_, err := NewCursor(opts)
	if err == nil {
		t.Fatal("NewCursor without Diag should fail")
	}
	if !strings.Contains(err.Error(), "Diag") {
		t.Errorf("error message should mention Diag: %v", err)
	}
}

// TestNewCursor_MissingSleep returns error when Sleep is nil.
func TestNewCursor_MissingSleep(t *testing.T) {
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: &fakeRunner{},
		Diag:    io.Discard,
		Sleep:   nil,
	}
	_, err := NewCursor(opts)
	if err == nil {
		t.Fatal("NewCursor without Sleep should fail")
	}
	if !strings.Contains(err.Error(), "Sleep") {
		t.Errorf("error message should mention Sleep: %v", err)
	}
}

// TestRequireCredential_LoggedIn succeeds when agent status returns 0.
func TestRequireCredential_LoggedIn(t *testing.T) {
	fakeSystemd := &fakeRunner{
		runFunc: func(name string, args ...string) (string, string, int, error) {
			if name == "/home/test/.local/bin/agent" && len(args) > 0 && args[0] == "status" {
				return `{"status":"authenticated","isAuthenticated":true}`, "", 0, nil
			}
			return "", "", 1, nil
		},
	}
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: fakeSystemd,
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	c, _ := NewCursor(opts)
	err := c.requireCredential()
	if err != nil {
		t.Errorf("requireCredential with status=0 should succeed, got: %v", err)
	}
}

// TestRequireCredential_NotLoggedIn fails when agent status returns non-zero.
func TestRequireCredential_NotLoggedIn(t *testing.T) {
	fakeSystemd := &fakeRunner{
		runFunc: func(name string, args ...string) (string, string, int, error) {
			if name == "/home/test/.local/bin/agent" && len(args) > 0 && args[0] == "status" {
				return "", "", 1, nil
			}
			return "", "", 1, nil
		},
	}
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: fakeSystemd,
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	c, _ := NewCursor(opts)
	err := c.requireCredential()
	if err == nil {
		t.Fatal("requireCredential with status=1 should fail")
	}
	if !errors.Is(err, errCursorNeedsLogin) {
		t.Errorf("requireCredential should return errCursorNeedsLogin, got: %v", err)
	}
}

// TestSetEnabled_PrintsDisclosure prints the paid-plan and data-flow
// disclosure when the head is switched on.
func TestSetEnabled_PrintsDisclosure(t *testing.T) {
	layout, err := paths.NewForHome(t.TempDir(), "codvps")
	if err != nil {
		t.Fatal(err)
	}
	diag := &bytes.Buffer{}
	c, err := NewCursor(CursorOptions{
		Layout: layout,
		Git:    &fakeRunner{},
		Systemd: &fakeRunner{runFunc: func(name string, args ...string) (string, string, int, error) {
			if len(args) > 0 && args[0] == "status" {
				return `{"status":"authenticated","isAuthenticated":true}`, "", 0, nil
			}
			return "", "", 0, nil
		}},
		Diag:  diag,
		Sleep: func(time.Duration) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := c.SetEnabled(true); err != nil {
		t.Fatalf("SetEnabled(true) = %v", err)
	}

	for _, want := range []string{
		"Cursor requires a Cursor plan with Cloud Agents",
		"code and context are sent to Cursor",
		"--worker-dir is not a sandbox",
	} {
		if !strings.Contains(diag.String(), want) {
			t.Errorf("disclosure %q not printed; got %q", want, diag.String())
		}
	}
}

func TestInstances_FiltersOutTemplate(t *testing.T) {
	fakeSystemd := &fakeRunner{
		runFunc: func(name string, args ...string) (string, string, int, error) {
			if name == "systemctl" && len(args) > 2 && args[2] == "cursor-remote@*.service" {
				// Return both template (should be filtered) and real instances
				return "cursor-remote@.service               disabled\ncursor-remote@repo1.service          enabled\ncursor-remote@repo2.service          enabled\n", "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	opts := CursorOptions{
		Layout:  createTestLayout(),
		Git:     &fakeRunner{},
		Systemd: fakeSystemd,
		Diag:    io.Discard,
		Sleep:   func(time.Duration) {},
	}
	c, _ := NewCursor(opts)
	instances, _ := c.instances(true)

	// Should have 2 instances (repo1, repo2), not the template
	if len(instances) != 2 {
		t.Errorf("instances should have 2 items, got %d", len(instances))
	}
	for _, inst := range instances {
		if inst.name == "" || inst.name == "." {
			t.Errorf("instances should not include template; got name=%q", inst.name)
		}
	}
}
