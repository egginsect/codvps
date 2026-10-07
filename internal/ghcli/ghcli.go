// Package ghcli installs and probes the current GitHub CLI. Host
// provisioning (`sudo codvps install`) and `codvps login github` share it:
// provisioning installs gh up front, and login installs or upgrades it on
// demand when the gh on the host is missing or too old for codvps.
package ghcli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitHub's apt repository for gh.
const (
	Keyring     = "/etc/apt/keyrings/githubcli-archive-keyring.gpg"
	SourcesList = "/etc/apt/sources.list.d/github-cli.list"
	KeyringURL  = "https://cli.github.com/packages/githubcli-archive-keyring.gpg"
	PackagesURL = "https://cli.github.com/packages"
)

// ProbeArgs is the gh invocation codvps depends on: `gh auth status
// --json` exists only in current gh releases, not in distro packages
// (Ubuntu 24.04 ships 2.45).
var ProbeArgs = []string{"auth", "status", "--json", "hosts"}

// State is what a probe of the gh on the host found.
type State int

// The probe outcomes.
const (
	// Usable means gh ran and understood the probe (logged in or not).
	Usable State = iota
	// Missing means there is no gh to run.
	Missing
	// TooOld means gh does not support `auth status --json`.
	TooOld
)

// Classify turns the result of running ProbeArgs into a State. Any other
// failure is Usable here: the caller reports it as gh's own error.
func Classify(stderr string, exitCode int, err error) State {
	switch {
	case err != nil && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)):
		return Missing
	case (exitCode != 0 || err != nil) && strings.Contains(stderr, "unknown flag: --json"):
		return TooOld
	}
	return Usable
}

// Host is what Install needs from its caller. Provisioning implements it
// as root; login implements it through sudo.
type Host interface {
	// Printf reports progress.
	Printf(format string, args ...any)
	// Run runs an unprivileged command with its output shown.
	Run(name string, args ...string) error
	// Root runs a command as root with its output shown.
	Root(name string, args ...string) error
	// Output runs a command and returns its stdout.
	Output(name string, args ...string) (string, error)
	// Exists reports whether a system path exists.
	Exists(path string) bool
	// Write writes a root-owned file, replacing a different one.
	Write(path string, data []byte, mode os.FileMode) error
}

// Install adds GitHub's apt repository (the keyring when absent, the source
// list GitHub documents) and installs gh. Installing over an older gh
// upgrades it to the repository's current release.
func Install(h Host) error {
	if !h.Exists(Keyring) {
		tmp, err := os.MkdirTemp("", "codvps-gh-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dl := filepath.Join(tmp, "keyring.gpg")
		if err := h.Run("curl", "-fsSL", "--proto", "=https", "-o", dl, KeyringURL); err != nil {
			return err
		}
		data, err := os.ReadFile(dl)
		if err != nil || len(data) == 0 {
			return fmt.Errorf("the GitHub CLI keyring download is empty or unreadable: %v", err)
		}
		if err := h.Write(Keyring, data, 0o644); err != nil {
			return err
		}
	}
	arch, err := h.Output("dpkg", "--print-architecture")
	arch = strings.TrimSpace(arch)
	if err != nil || arch == "" {
		return fmt.Errorf("dpkg --print-architecture failed: %v", err)
	}
	list := fmt.Sprintf("deb [arch=%s signed-by=%s] %s stable main\n", arch, Keyring, PackagesURL)
	if err := h.Write(SourcesList, []byte(list), 0o644); err != nil {
		return err
	}
	apt := []string{"DEBIAN_FRONTEND=noninteractive", "apt-get"}
	if err := h.Root("env", append(apt, "update")...); err != nil {
		return err
	}
	return h.Root("env", append(apt, "install", "-y", "gh")...)
}
