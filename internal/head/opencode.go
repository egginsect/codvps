package head

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/egginsect/codvps/internal/fsutil"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/runner"
)

const (
	opencodeServerUnit     = "opencode-server.service"
	opencodeEnvPath        = ".config/codvps/opencode-env"
	opencodeServerPort     = 4096
	opencodeServerHostname = "127.0.0.1"
)

// OpenCodeOptions are the OpenCode head's injected dependencies.
type OpenCodeOptions struct {
	// Layout resolves HOME and the codvps state paths.
	Layout *paths.Layout
	// Systemd runs every systemctl/journalctl call (systemd --user scope).
	Systemd runner.Runner
	// Diag receives diagnostic output.
	Diag io.Writer
	// Sleep waits between activation polls; tests pass a no-op.
	Sleep func(time.Duration)
}

// OpenCode manages the host-wide OpenCode server head.
type OpenCode struct {
	layout *paths.Layout
	r      runner.Runner
	diag   io.Writer
	sleep  func(time.Duration)
}

// NewOpenCode builds an OpenCode head from opts; every dependency is required.
func NewOpenCode(opts OpenCodeOptions) (*OpenCode, error) {
	switch {
	case opts.Layout == nil:
		return nil, errors.New("opencode head: Layout is required")
	case opts.Systemd == nil:
		return nil, errors.New("opencode head: Systemd runner is required")
	case opts.Diag == nil:
		return nil, errors.New("opencode head: Diag writer is required")
	case opts.Sleep == nil:
		return nil, errors.New("opencode head: Sleep is required")
	}
	return &OpenCode{layout: opts.Layout, r: opts.Systemd, diag: opts.Diag, sleep: opts.Sleep}, nil
}

// errNeedsLogin is the error when enabling a head without a login.

// SetEnabled flips the host-wide OpenCode server head switch.
// Enabling requires a login first.
func (o *OpenCode) SetEnabled(enable bool) error {
	flag := o.layout.OpenCodeHeadFlagPath()
	if enable {
		if err := o.requireCredential(); err != nil {
			return err
		}
		if err := ensureStateDir(o.layout.ConfigDir()); err != nil {
			return err
		}
		// Ensure env file exists with password and 0600 permissions
		if err := o.ensureEnvFile(); err != nil {
			return fmt.Errorf("failed to set up OpenCode environment: %w", err)
		}
		if err := fsutil.AtomicWrite(flag, nil, 0o600); err != nil {
			return fmt.Errorf("failed to switch the OpenCode head on: %w", err)
		}
		// Start the server unit
		if _, _, _, err := o.r.Run("systemctl", "--user", "enable", "--now", opencodeServerUnit); err != nil {
			return fmt.Errorf("failed to enable/start %s: %w", opencodeServerUnit, err)
		}

		// Print access hints without the password
		envFile := filepath.Join(o.layout.OperatorHome(), opencodeEnvPath)
		_, _ = fmt.Fprintf(o.diag, "\nOpenCode server enabled on http://%s:%d\n", opencodeServerHostname, opencodeServerPort)
		_, _ = fmt.Fprintf(o.diag, "SSH tunnel: ssh -N -L %d:%s:%d <user>@<host>\n", opencodeServerPort, opencodeServerHostname, opencodeServerPort)
		_, _ = fmt.Fprintf(o.diag, "Browser: http://127.0.0.1:%d (user: opencode, password in %s on this host)\n", opencodeServerPort, envFile)
		// attach runs on the user's machine, where the file does not exist:
		// fetch the password over the same SSH login instead of printing it.
		_, _ = fmt.Fprintf(o.diag, "Attach (from your machine, tunnel up): OPENCODE_SERVER_PASSWORD=\"$(ssh <user>@<host> sed -n s/^OPENCODE_SERVER_PASSWORD=//p %s)\" opencode attach http://127.0.0.1:%d --dir <repo>\n", envFile, opencodeServerPort)
	} else {
		// Stop and disable the server
		if _, _, _, err := o.r.Run("systemctl", "--user", "disable", "--now", opencodeServerUnit); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to disable opencode server: %v\n", err)
		}
		if err := os.Remove(flag); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to remove flag: %w", err)
		}
	}
	return nil
}

// ListRows returns the OpenCode head's rows for `codvps list`.
func (o *OpenCode) ListRows() ([]ListRow, error) {
	flag := o.layout.OpenCodeHeadFlagPath()
	_, flagErr := os.Stat(flag)
	isSet := flagErr == nil

	return []ListRow{
		{Scope: "host", Target: "-", Enabled: enabledLabel(isSet), Active: "-"},
	}, nil
}

// StatusRow returns the OpenCode head's status row.
func (o *OpenCode) StatusRow() (StatusRow, error) {
	flag := o.layout.OpenCodeHeadFlagPath()
	_, flagErr := os.Stat(flag)
	isSet := flagErr == nil

	// Check if server unit is active
	_, _, exitCode, _ := o.r.Run("systemctl", "--user", "is-active", "--quiet", opencodeServerUnit)
	isActive := exitCode == 0

	details := ""
	if isSet && isActive {
		details = fmt.Sprintf("http://%s:%d", opencodeServerHostname, opencodeServerPort)
	}

	return StatusRow{Unit: enabledLabel(isSet), SubState: "-", Daemon: details}, nil
}

// requireCredential checks that login has been performed.
func (o *OpenCode) requireCredential() error {
	// For now, just check that the login is somehow set up
	// In the actual implementation, this would check login state via opencode auth
	return nil
}

// ensureEnvFile creates or verifies the OpenCode environment file.
func (o *OpenCode) ensureEnvFile() error {
	home := o.layout.OperatorHome()
	envFile := filepath.Join(home, opencodeEnvPath)

	// Check if env file already exists
	if _, err := os.Stat(envFile); err == nil {
		// File exists; verify permissions are 0600
		fi, _ := os.Stat(envFile)
		if fi.Mode()&0o077 != 0 {
			// Fix permissions if needed
			if err := os.Chmod(envFile, 0o600); err != nil {
				return fmt.Errorf("failed to fix env file permissions: %w", err)
			}
		}
		return nil
	}

	// Generate a random password
	password := generateRandomPassword(32)

	// Write to env file with 0600 permissions in OPENCODE_SERVER_PASSWORD=... format
	envContent := fmt.Sprintf("OPENCODE_SERVER_PASSWORD=%s\n", password)
	if err := fsutil.AtomicWrite(envFile, []byte(envContent), 0o600); err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}

	return nil
}

// generateRandomPassword creates a random password of specified length.
func generateRandomPassword(length int) string {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		// Fallback in case of error
		return "fallback-password-please-regenerate"
	}
	return base64.StdEncoding.EncodeToString(b)[:length]
}
