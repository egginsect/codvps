package cli

import (
	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRepoAddLocalBypassesGitHubAndHeadsInDefaultMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "CODVPS_NAMESPACE"} {
		t.Setenv(key, "")
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	// Do not configure manual mode: offline registration must bypass even the
	// default systemd manager, provider inventory and authentication probes.
	dest := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", "https://github.com/owner/private-offline.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dest
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "forbidden-called")
	for _, name := range []string{"gh", "systemctl", "sudo", "runuser", "claude", "codex", "agent"} {
		script := "#!/bin/sh\nprintf called > '" + marker + "'\nexit 99\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := Execute([]string{"repo", "add-local", dest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("called gh/provider/service command")
	}
	layout, err := paths.New("")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil || len(entries) != 1 || entries[0].Path != dest {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}
func TestRepoAddLocalRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"repo", "add-local"}, {"repo", "add-local", "/tmp/repo", "--dest", "/tmp/other"}} {
		if err := Execute(args); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
