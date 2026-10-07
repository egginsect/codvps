package paths

import (
	"path/filepath"
	"testing"
)

func TestLayoutNew(t *testing.T) {
	tests := []struct {
		name      string
		home      string
		namespace string
		wantErr   bool
	}{
		{"valid with default namespace", "/home/user", "", false},
		{"valid with custom namespace", "/home/user", "test", false},
		{"valid with dash in namespace", "/home/user", "test-1", false},
		{"empty home should fail", "", "", true},
		{"invalid namespace with uppercase", "/home/user", "Test", true},
		{"invalid namespace with underscore", "/home/user", "test_1", true},
		{"invalid namespace with dot", "/home/user", "test.1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set env
			if tt.home != "" {
				t.Setenv("HOME", tt.home)
			} else {
				t.Setenv("HOME", "")
			}
			t.Setenv("CODVPS_NAMESPACE", "")

			layout, err := New(tt.namespace)
			if (err != nil) != tt.wantErr {
				t.Errorf("New() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && layout == nil {
				t.Errorf("New() returned nil layout")
			}
		})
	}
}

func TestLayoutPaths(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_STATE_HOME", "/custom/state")

	layout, _ := New("codvps")

	tests := []struct {
		name     string
		method   func(*Layout) string
		expected string
	}{
		{"ConfigDir", func(l *Layout) string { return l.ConfigDir() }, "/custom/config/codvps"},
		{"StateDir", func(l *Layout) string { return l.StateDir() }, "/custom/state/codvps"},
		{"RepositoriesPath", func(l *Layout) string { return l.RepositoriesPath() }, "/custom/config/codvps/repositories"},
		{"CodexRepositoriesPath", func(l *Layout) string { return l.CodexRepositoriesPath() }, "/custom/config/codvps/codex-repositories"},
		{"RuntimeRootPath", func(l *Layout) string { return l.RuntimeRootPath() }, "/opt/codvps/runtimes"},
		{"RunPath", func(l *Layout) string { return l.RunPath() }, "/run/codvps"},
		{"LibexecPath", func(l *Layout) string { return l.LibexecPath() }, "/usr/local/libexec/codvps"},
		{"EtcPath", func(l *Layout) string { return l.EtcPath() }, "/etc/codvps"},
		{"Home", func(l *Layout) string { return l.Home() }, "/home/user"},
		{"Namespace", func(l *Layout) string { return l.Namespace() }, "codvps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.method(layout)
			if result != tt.expected {
				t.Errorf("%s() = %q, want %q", tt.name, result, tt.expected)
			}
		})
	}
}

func TestLayoutWithNamespace(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	layout, _ := New("test-ns")

	tests := []struct {
		name     string
		method   func(*Layout) string
		expected string
	}{
		{"ConfigDir", func(l *Layout) string { return l.ConfigDir() }, filepath.Join("/home/user", ".config", "test-ns")},
		{"RuntimeRootPath", func(l *Layout) string { return l.RuntimeRootPath() }, "/opt/test-ns/runtimes"},
		{"UnitNamePrefix", func(l *Layout) string { return l.UnitNamePrefix() }, "test-ns"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.method(layout)
			if result != tt.expected {
				t.Errorf("%s() = %q, want %q", tt.name, result, tt.expected)
			}
		})
	}
}

func TestDefaultXDGPaths(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	layout, _ := New("codvps")

	tests := []struct {
		name     string
		method   func(*Layout) string
		expected string
	}{
		{"ConfigDir default", func(l *Layout) string { return l.ConfigDir() }, "/home/user/.config/codvps"},
		{"StateDir default", func(l *Layout) string { return l.StateDir() }, "/home/user/.local/state/codvps"},
		{"CacheDir default", func(l *Layout) string { return l.CacheDir() }, "/home/user/.cache/codvps"},
		{"DataDir default", func(l *Layout) string { return l.DataDir() }, "/home/user/.local/share/codvps"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.method(layout)
			if result != tt.expected {
				t.Errorf("%s() = %q, want %q", tt.name, result, tt.expected)
			}
		})
	}
}

func TestWithSystemRootPrefixesOnlySystemPaths(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CODVPS_NAMESPACE", "")
	base, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	l := base.WithSystemRoot(root)

	want := map[string]string{
		"EtcPath":         root + "/etc/codvps",
		"RuntimeRootPath": root + "/opt/codvps/runtimes",
		"RunPath":         root + "/run/codvps",
		"LibexecPath":     root + "/usr/local/libexec/codvps",
		"ConfigDir":       "/home/user/.config/codvps",
	}
	got := map[string]string{
		"EtcPath":         l.EtcPath(),
		"RuntimeRootPath": l.RuntimeRootPath(),
		"RunPath":         l.RunPath(),
		"LibexecPath":     l.LibexecPath(),
		"ConfigDir":       l.ConfigDir(),
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
	if base.EtcPath() != "/etc/codvps" {
		t.Errorf("WithSystemRoot mutated the original layout: %q", base.EtcPath())
	}
}

// NewForHome must ignore the caller's own HOME/XDG_* (a sudo session's are
// root's) and derive every operator path from the named home alone.
func TestNewForHomeIgnoresCallerEnvironment(t *testing.T) {
	t.Setenv("HOME", "/root")
	t.Setenv("XDG_CONFIG_HOME", "/root/.xdg-config")
	l, err := NewForHome("/home/user", "")
	if err != nil {
		t.Fatalf("NewForHome: %v", err)
	}
	if got, want := l.ConfigDir(), "/home/user/.config/codvps"; got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := l.Home(), "/home/user"; got != want {
		t.Errorf("Home = %q, want %q", got, want)
	}
	if got := l.Namespace(); got != DefaultNamespace {
		t.Errorf("Namespace = %q, want %q", got, DefaultNamespace)
	}
	for _, bad := range []string{"", "relative/home"} {
		if _, err := NewForHome(bad, ""); err == nil {
			t.Errorf("NewForHome(%q) accepted a non-absolute home", bad)
		}
	}
	if _, err := NewForHome("/home/user", "Bad_NS"); err == nil {
		t.Error("NewForHome accepted an invalid namespace")
	}
}
