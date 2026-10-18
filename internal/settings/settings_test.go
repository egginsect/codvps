package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
)

func TestDefaultsAndRoundTrip(t *testing.T) {
	layout, err := paths.NewForHome(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := RepositoryRoot(layout)
	if err != nil || root != layout.Home() {
		t.Fatalf("default root=%q err=%v", root, err)
	}
	cfg := Config{RepoRoot: "/workspace/shared/repos", GitHubConfigDir: "/workspace/auth", GitConfigGlobal: "/workspace/auth/gitconfig", SessionMode: "manual"}
	if err := Write(layout, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Read(layout)
	if err != nil || got != cfg {
		t.Fatalf("read=%+v err=%v", got, err)
	}
	env, err := Environment(layout)
	want := []string{"GH_CONFIG_DIR=/workspace/auth", "GIT_CONFIG_GLOBAL=/workspace/auth/gitconfig"}
	if err != nil || !reflect.DeepEqual(env, want) {
		t.Fatalf("env=%q err=%v", env, err)
	}
	info, err := os.Stat(Path(layout))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("file permissions: %v %v", info, err)
	}
}

func TestInvalidConfigurationFailsClosed(t *testing.T) {
	for _, body := range []string{`{"repo_root":"relative"}`, `{"repo_root":"/a/../b"}`, `{"repo_root":"/a\nb"}`, `{"session_mode":"unknown"}`, `{"unknown":"x"}`, `{} {}`, `[{}]`, `null`, `{"repo_root":null}`, `{"repo_root":"/first","repo_root":"/second"}`, `{"Repo_Root":"/capital"}`, strings.Repeat(" ", maxBytes+1)} {
		t.Run(body[:min(len(body), 50)], func(t *testing.T) {
			layout, _ := paths.NewForHome(t.TempDir(), "")
			if err := os.MkdirAll(layout.ConfigDir(), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(layout), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(layout); err == nil {
				t.Fatal("accepted invalid settings")
			}
		})
	}
}

func TestRefusesSettingsSymlinkAndDoesNotReadCredentialFiles(t *testing.T) {
	layout, _ := paths.NewForHome(t.TempDir(), "")
	cfg := Config{GitHubConfigDir: filepath.Join(t.TempDir(), "missing-secret-directory"), GitConfigGlobal: "/missing/gitconfig"}
	if err := Write(layout, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := Environment(layout); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "other.json")
	if err := os.Rename(Path(layout), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path(layout)); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(layout); err == nil {
		t.Fatal("accepted symlink")
	}
}
