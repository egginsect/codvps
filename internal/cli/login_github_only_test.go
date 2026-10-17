package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

func TestLoginGitHubAuthOnlyRejectsLegacyFlagsBeforeSetup(t *testing.T) {
	for _, flags := range [][]string{
		{"--key", "skip"}, {"--key="}, {"--passphrase-empty"},
		{"--passphrase-empty=false"}, {"--identity-name", "Test"},
		{"--identity-name="}, {"--identity-email", "test@example.com"},
		{"--identity-email="}, {"--yes"}, {"--yes=false"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			err := handleLoginGitHubWithContext(append([]string{"--auth-only"}, flags...), func(*paths.Layout) (*login.Context, error) {
				t.Fatal("incompatible flags reached login setup")
				return nil, nil
			})
			var exitErr *ExitError
			if !errors.As(err, &exitErr) || exitErr.Code != 2 || !strings.Contains(err.Error(), "--auth-only cannot be combined") {
				t.Fatalf("error = %v; want incompatible flags with exit 2", err)
			}
		})
	}
}

func TestLoginGitHubAuthOnlyDispatchUsesOnlyReadOnlyCommands(t *testing.T) {
	for _, mode := range []string{"--non-interactive", "--defer"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fr := runner.NewFakeRunner()
			fr.SetResponse("gh", []string{"api", "--hostname", "github.com", "user", "--jq", ".login"}, runner.Response{Stdout: "octocat\n"})
			err := handleLoginGitHubWithContext([]string{"--auth-only", mode}, func(layout *paths.Layout) (*login.Context, error) {
				return &login.Context{Runner: fr, HomeDir: layout.Home(), Stdout: io.Discard, Stderr: io.Discard}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			want := []runner.Call{
				{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com"}},
				{Name: "gh", Args: []string{"api", "--hostname", "github.com", "user", "--jq", ".login"}},
			}
			if !reflect.DeepEqual(fr.Calls, want) {
				t.Fatalf("calls = %v; want %v", fr.Calls, want)
			}
		})
	}
}

func TestLoginGitHubAuthOnlyUnauthenticatedExitCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fr := runner.NewFakeRunner()
	fr.SetResponse("gh", []string{"auth", "status", "--hostname", "github.com"}, runner.Response{ExitCode: 1})
	err := handleLoginGitHubWithContext([]string{"--auth-only", "--non-interactive"}, func(*paths.Layout) (*login.Context, error) {
		return &login.Context{Runner: fr, Stdout: io.Discard, Stderr: io.Discard}, nil
	})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 || !strings.Contains(err.Error(), "codvps login github --auth-only") {
		t.Fatalf("error = %v", err)
	}
	if want := []runner.Call{{Name: "gh", Args: []string{"auth", "status", "--hostname", "github.com"}}}; !reflect.DeepEqual(fr.Calls, want) {
		t.Fatalf("calls = %v; want %v", fr.Calls, want)
	}
}

func TestLoginGitHubLegacyDeferStillDispatchesLegacyFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fr := runner.NewFakeRunner()
	err := handleLoginGitHubWithContext([]string{"--defer"}, func(layout *paths.Layout) (*login.Context, error) {
		return &login.Context{Runner: fr, HomeDir: layout.Home(), Stdout: io.Discard, Stderr: io.Discard}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []runner.Call{
		{Name: "gh", Args: []string{"auth", "status", "--json", "hosts"}},
		{Name: "git", Args: []string{"config", "--global", "--get", "user.name"}},
		{Name: "git", Args: []string{"config", "--global", "--get", "user.email"}},
	}
	if !reflect.DeepEqual(fr.Calls, want) {
		t.Fatalf("calls = %v; want %v", fr.Calls, want)
	}
}

func TestGitHubLoginContextScopesConfiguredPathsToEveryRunner(t *testing.T) {
	home := t.TempDir()
	layout, err := paths.NewForHome(home, "test")
	if err != nil {
		t.Fatal(err)
	}
	config := settings.Config{
		GitHubConfigDir: filepath.Join(home, "credentials", "gh"),
		GitConfigGlobal: filepath.Join(home, "git", "config"),
	}
	if err := settings.Write(layout, config); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_CONFIG_DIR", "/parent/gh")
	t.Setenv("GIT_CONFIG_GLOBAL", "/parent/gitconfig")
	var environments [][]string
	ctx, err := newGitHubLoginContext(layout, func(env ...string) runner.Runner {
		environments = append(environments, append([]string(nil), env...))
		return runner.NewFakeRunner()
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx.EnvRunner([]string{"EXTRA=one"})
	ctx.EnvRunner([]string{"EXTRA=two"})
	want := [][]string{
		{"GH_CONFIG_DIR=" + config.GitHubConfigDir, "GIT_CONFIG_GLOBAL=" + config.GitConfigGlobal},
		{"GH_CONFIG_DIR=" + config.GitHubConfigDir, "GIT_CONFIG_GLOBAL=" + config.GitConfigGlobal, "EXTRA=one"},
		{"GH_CONFIG_DIR=" + config.GitHubConfigDir, "GIT_CONFIG_GLOBAL=" + config.GitConfigGlobal, "EXTRA=two"},
	}
	if !reflect.DeepEqual(environments, want) {
		t.Fatalf("child environments = %v; want %v", environments, want)
	}
	if ctx.HomeDir != home || os.Getenv("GH_CONFIG_DIR") != "/parent/gh" || os.Getenv("GIT_CONFIG_GLOBAL") != "/parent/gitconfig" {
		t.Fatal("login context changed the process environment or resolved the wrong home")
	}
	for _, path := range []string{config.GitHubConfigDir, config.GitConfigGlobal} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("login context touched configured credential path %q: %v", path, err)
		}
	}
}

func TestGitHubLoginContextInvalidSettingsNeverCreatesRunner(t *testing.T) {
	layout, err := paths.NewForHome(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(layout), []byte(`{"github_config_dir":"relative"}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = newGitHubLoginContext(layout, func(...string) runner.Runner {
		t.Fatal("invalid settings created a subprocess runner")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "failed to read login settings") {
		t.Fatalf("error = %v", err)
	}
}
