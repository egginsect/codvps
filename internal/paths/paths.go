// Package paths resolves codvps filesystem paths.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Layout resolves every codvps path from injected HOME/XDG env and optional CODVPS_NAMESPACE override.
type Layout struct {
	home      string
	xdgConfig string
	xdgState  string
	xdgCache  string
	xdgData   string
	namespace string
	// systemRoot prefixes every system-wide path (/etc, /opt, /run,
	// /usr/local/libexec). It is empty in production and is set only in
	// code via WithSystemRoot, never from the environment, so a caller's
	// environment cannot redirect root-owned paths.
	systemRoot string
}

// New creates a Layout from environment variables.
// HOME and XDG_* env vars are used; CODVPS_NAMESPACE is optional and defaults to "codvps".
// If namespace is provided, it overrides the environment variable.
func New(namespace string) (*Layout, error) {
	home := os.Getenv("HOME")
	if home == "" {
		return nil, fmt.Errorf("HOME environment variable not set")
	}

	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}

	xdgState := os.Getenv("XDG_STATE_HOME")
	if xdgState == "" {
		xdgState = filepath.Join(home, ".local", "state")
	}

	xdgCache := os.Getenv("XDG_CACHE_HOME")
	if xdgCache == "" {
		xdgCache = filepath.Join(home, ".cache")
	}

	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(home, ".local", "share")
	}

	// Use provided namespace or get from environment, defaulting to "codvps"
	ns := namespace
	if ns == "" {
		ns = os.Getenv("CODVPS_NAMESPACE")
		if ns == "" {
			ns = DefaultNamespace
		}
	}

	// Validate namespace: [a-z0-9-]
	if !isValidNamespace(ns) {
		return nil, fmt.Errorf("invalid CODVPS_NAMESPACE: %q (must match [a-z0-9-])", ns)
	}

	return &Layout{
		home:      home,
		xdgConfig: xdgConfig,
		xdgState:  xdgState,
		xdgCache:  xdgCache,
		xdgData:   xdgData,
		namespace: ns,
	}, nil
}

// DefaultNamespace is the namespace every production install uses.
const DefaultNamespace = "codvps"

// NewForHome builds a Layout for an explicitly named home directory,
// ignoring the caller's own HOME and XDG_* variables. Root-side commands
// (install, uninstall) use it to address the operator's state from a sudo
// session, where HOME and XDG_* describe root rather than the operator.
// An empty namespace means DefaultNamespace.
func NewForHome(home, namespace string) (*Layout, error) {
	if home == "" || !filepath.IsAbs(home) {
		return nil, fmt.Errorf("home directory must be an absolute path, got %q", home)
	}
	if namespace == "" {
		namespace = DefaultNamespace
	}
	if !isValidNamespace(namespace) {
		return nil, fmt.Errorf("invalid CODVPS_NAMESPACE: %q (must match [a-z0-9-])", namespace)
	}
	return &Layout{
		home:      home,
		xdgConfig: filepath.Join(home, ".config"),
		xdgState:  filepath.Join(home, ".local", "state"),
		xdgCache:  filepath.Join(home, ".cache"),
		xdgData:   filepath.Join(home, ".local", "share"),
		namespace: namespace,
	}, nil
}

// WithSystemRoot returns a copy of l whose system-wide paths live under
// root instead of /. Tests use it to keep /etc, /opt, /run and
// /usr/local/libexec inside a temporary directory.
func (l *Layout) WithSystemRoot(root string) *Layout {
	copied := *l
	copied.systemRoot = root
	return &copied
}

// system joins a system-wide path under the configured root.
func (l *Layout) system(parts ...string) string {
	return filepath.Join(append([]string{l.systemRoot, "/"}, parts...)...)
}

// SystemPath resolves an absolute production system path (for example a
// unit file under /etc/systemd) under the configured system root.
func (l *Layout) SystemPath(path string) string {
	return l.system(path)
}

// isValidNamespace checks if name matches [a-z0-9-]
func isValidNamespace(name string) bool {
	if name == "" {
		return false
	}
	matched, _ := regexp.MatchString(`^[a-z0-9-]+$`, name)
	return matched
}

// ConfigDir returns ~/.config/codvps or ~/.config/{CODVPS_NAMESPACE}
func (l *Layout) ConfigDir() string {
	return filepath.Join(l.xdgConfig, l.namespace)
}

// StateDir returns ~/.local/state/codvps or ~/.local/state/{CODVPS_NAMESPACE}
func (l *Layout) StateDir() string {
	return filepath.Join(l.xdgState, l.namespace)
}

// CacheDir returns ~/.cache/codvps or ~/.cache/{CODVPS_NAMESPACE}
func (l *Layout) CacheDir() string {
	return filepath.Join(l.xdgCache, l.namespace)
}

// DataDir returns ~/.local/share/codvps or ~/.local/share/{CODVPS_NAMESPACE}
func (l *Layout) DataDir() string {
	return filepath.Join(l.xdgData, l.namespace)
}

// RepositoriesPath returns ~/.config/codvps/repositories
func (l *Layout) RepositoriesPath() string {
	return filepath.Join(l.ConfigDir(), "repositories")
}

// CodexRepositoriesPath returns ~/.config/codvps/codex-repositories, the
// membership manifest of the retired Codex sandbox; install removes it.
func (l *Layout) CodexRepositoriesPath() string {
	return filepath.Join(l.ConfigDir(), "codex-repositories")
}

// RuntimeRootPath returns /opt/codvps/runtimes or /opt/{CODVPS_NAMESPACE}/runtimes
func (l *Layout) RuntimeRootPath() string {
	return l.system("opt", l.namespace, "runtimes")
}

// RunPath returns /run/codvps or /run/{CODVPS_NAMESPACE}
func (l *Layout) RunPath() string {
	return l.system("run", l.namespace)
}

// LibexecPath returns /usr/local/libexec/codvps or /usr/local/libexec/{CODVPS_NAMESPACE}
func (l *Layout) LibexecPath() string {
	return l.system("usr", "local", "libexec", l.namespace)
}

// EtcPath returns /etc/codvps or /etc/{CODVPS_NAMESPACE}
func (l *Layout) EtcPath() string {
	return l.system("etc", l.namespace)
}

// Namespace returns the current namespace
func (l *Layout) Namespace() string {
	return l.namespace
}

// Home returns the home directory
func (l *Layout) Home() string {
	return l.home
}

// UnitNamePrefix returns the systemd unit name prefix (e.g. "codvps", "test", etc.)
func (l *Layout) UnitNamePrefix() string {
	return l.namespace
}

// ClaudeHeadFlagPath returns ~/.config/codvps/claude-head-enabled: the
// host-wide Claude head switch (the file's presence means enabled).
func (l *Layout) ClaudeHeadFlagPath() string {
	return filepath.Join(l.ConfigDir(), "claude-head-enabled")
}

// ClaudeCredentialsPath returns ~/.claude/.credentials.json, the Claude
// CLI's stored OAuth login that every Claude head runs on.
func (l *Layout) ClaudeCredentialsPath() string {
	return filepath.Join(l.home, ".claude", ".credentials.json")
}

// CursorHeadFlagPath returns ~/.config/codvps/cursor-head-enabled: the
// host-wide Cursor head switch (the file's presence means enabled).
func (l *Layout) CursorHeadFlagPath() string {
	return filepath.Join(l.ConfigDir(), "cursor-head-enabled")
}

// OpenCodeHeadFlagPath returns ~/.config/codvps/opencode-head-enabled: the
// host-wide OpenCode head switch (the file's presence means enabled).
func (l *Layout) OpenCodeHeadFlagPath() string {
	return filepath.Join(l.ConfigDir(), "opencode-head-enabled")
}

// OperatorHome returns the operator's home directory (for direct access in heads).
func (l *Layout) OperatorHome() string {
	return l.home
}
