package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/components"
	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/providers"
	"github.com/egginsect/codvps/internal/runner"
)

func hermeticLayout(t *testing.T) *paths.Layout {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, v := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "CODVPS_NAMESPACE"} {
		t.Setenv(v, "")
	}
	layout, err := paths.New("")
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	// Keep /etc/codvps/components.json inside the test.
	return layout.WithSystemRoot(t.TempDir())
}

func testHeadEnv(t *testing.T, layout *paths.Layout, system, codex runner.Runner) *headEnv {
	t.Helper()
	env, err := buildHeadEnv(headDeps{
		layout:    layout,
		git:       runner.NewFakeRunner(),
		system:    system,
		exec:      func(...string) runner.Runner { return codex },
		providers: providers.All(),
		operator:  "operator",
		sleep:     func(time.Duration) {},
		now:       time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// `head enable <claude|codex>` is refused, before any systemd call, when
// the provider is not a selected component; `head disable` is never gated.
func TestHeadSetEnabledGating(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			layout := hermeticLayout(t)
			fr := runner.NewFakeRunner()
			codex := runner.NewFakeRunner()
			env := testHeadEnv(t, layout, fr, codex)

			err := env.headSetEnabled(provider, true)
			want := provider + " is not a selected component; rerun with it selected, e.g.: sudo codvps install --components claude,codex,cursor,opencode"
			if err == nil || err.Error() != want {
				t.Fatalf("head enable %s = %v, want %q", provider, err, want)
			}
			if len(fr.Calls) != 0 || len(codex.Calls) != 0 {
				t.Fatalf("a refused enable reached systemd or codex: %v %v", fr.Calls, codex.Calls)
			}

			if err := env.headSetEnabled(provider, false); err != nil {
				t.Fatalf("head disable %s must not be gated: %v", provider, err)
			}
		})
	}
}

// Reference test_components.sh login_codex on a claude-only host: login
// codex is refused with the actionable message before codex ever runs.
func TestLoginCodexRefusedWhenCodexUnselected(t *testing.T) {
	layout := hermeticLayout(t)
	if err := components.Write(components.RegistryPath(layout), &components.Registry{Version: components.RegistryVersion, CodingCLIs: []string{"claude"}, Switch: "none"}); err != nil {
		t.Fatal(err)
	}
	fr := runner.NewFakeRunner()
	err := runLogin(layout, providers.All(), "codex", &login.Context{Runner: fr, HomeDir: layout.Home()})
	want := "codex is not a selected component; select it first, e.g.: sudo codvps install --components claude,codex,cursor,opencode"
	if err == nil || err.Error() != want {
		t.Fatalf("login codex = %v, want %q", err, want)
	}
	if len(fr.Calls) != 0 {
		t.Fatalf("a refused login ran %v", fr.Calls)
	}
}

// TestInteractiveToolsUsesInstallPaths: a CLI installed outside PATH is
// reported from its home-relative install path, others by name.
func TestInteractiveToolsUsesInstallPaths(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".opencode", "bin", "opencode")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tool := range interactiveTools(providers.All(), home) {
		got[tool.Label] = tool.Binary
	}
	if got["OpenCode"] != bin {
		t.Errorf("OpenCode binary = %q, want %q", got["OpenCode"], bin)
	}
	if got["Cursor"] != "agent" || got["Claude"] != "claude" {
		t.Errorf("tools = %v; want cursor and claude by name when not installed under home", got)
	}
}
