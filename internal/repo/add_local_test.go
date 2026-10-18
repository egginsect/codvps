package repo

import (
	"fmt"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type offlineGit struct {
	t      *testing.T
	real   runner.Runner
	calls  [][]string
	before func([]string)
}

func (g *offlineGit) Run(name string, args ...string) (string, string, int, error) {
	g.t.Helper()
	if name != "git" || len(args) < 4 || args[0] != "-C" || (args[2] != "rev-parse" && !(len(args) == 5 && reflect.DeepEqual(args[2:], []string{"remote", "get-url", "origin"}))) {
		g.t.Fatalf("nonlocal command forbidden: %s %v", name, args)
	}
	g.calls = append(g.calls, append([]string{}, args...))
	if g.before != nil {
		g.before(args)
	}
	return g.real.Run(name, args...)
}
func (g *offlineGit) RunWithIO(name string, args []string, in io.Reader, out, diag io.Writer) (int, error) {
	g.t.Fatalf("stream command forbidden: %s %v", name, args)
	return 1, fmt.Errorf("forbidden")
}
func localCheckout(t *testing.T, origin string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if origin != "" {
		runGit(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func TestAddLocalOfflinePreservesCheckoutAndNeverStartsHeads(t *testing.T) {
	for _, origin := range []string{"https://github.com/Owner/Private.git", "git@github.com:Owner/Private.git", "ssh://git@github.com/Owner/Private.git"} {
		t.Run(origin, func(t *testing.T) {
			layout := newTestLayout(t)
			dest := localCheckout(t, origin)
			marker := filepath.Join(dest, "dirty.txt")
			os.WriteFile(marker, []byte("keep"), 0600)
			g := &offlineGit{t: t, real: runner.NewExecRunner()}
			head := newFakeHeadReconciler()
			rm := NewRepoManager(layout, g, fakeHeads(head))
			for i := 0; i < 2; i++ {
				if err := rm.AddLocal(dest); err != nil {
					t.Fatal(err)
				}
			}
			entries, err := registry.ReadEntries(layout.RepositoriesPath())
			if err != nil || len(entries) != 1 || entries[0].Name != "Private" || entries[0].Path != dest {
				t.Fatalf("entries=%v err=%v", entries, err)
			}
			if len(head.Calls) != 0 {
				t.Fatal("started heads", head.Calls)
			}
			data, _ := os.ReadFile(marker)
			if string(data) != "keep" {
				t.Fatal("checkout modified")
			}
		})
	}
}
func TestAddLocalRejectsNonGitHubOrUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"", "https://gitlab.com/owner/repo.git", "https://github.com.evil.test/owner/repo", "https://user:secret@github.com/owner/repo", "https://github.com/owner/repo?query=x", "ssh://git@github.com:22/owner/repo", "ext::arbitrary command", "/local/repo", "https://github.com/owner/.."} {
		t.Run(origin, func(t *testing.T) {
			layout := newTestLayout(t)
			dest := localCheckout(t, origin)
			g := &offlineGit{t: t, real: runner.NewExecRunner()}
			if NewRepoManager(layout, g, nil).AddLocal(dest) == nil {
				t.Fatal("accepted origin")
			}
			entries, _ := registry.ReadEntries(layout.RepositoriesPath())
			if len(entries) != 0 {
				t.Fatal("registered invalid origin")
			}
		})
	}
}
func TestAddLocalMissingNeverClones(t *testing.T) {
	layout := newTestLayout(t)
	dest := filepath.Join(t.TempDir(), "missing")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	if NewRepoManager(layout, g, nil).AddLocal(dest) == nil {
		t.Fatal("accepted missing")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("created checkout")
	}
	if len(g.calls) != 0 {
		t.Fatal("ran git for missing path")
	}
}
func TestAddLocalRejectsNameCollision(t *testing.T) {
	layout := newTestLayout(t)
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	rm := NewRepoManager(layout, g, nil)
	a := localCheckout(t, "https://github.com/first/repo.git")
	b := localCheckout(t, "https://github.com/second/repo.git")
	if err := rm.AddLocal(a); err != nil {
		t.Fatal(err)
	}
	if err := rm.AddLocal(b); err == nil || !strings.Contains(err.Error(), "remap") {
		t.Fatalf("collision: %v", err)
	}
}
func TestAddLocalRejectsSymlinkAndGitfile(t *testing.T) {
	layout := newTestLayout(t)
	dest := localCheckout(t, "https://github.com/owner/repo.git")
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dest, link)
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	rm := NewRepoManager(layout, g, nil)
	if rm.AddLocal(link) == nil {
		t.Fatal("accepted symlink")
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, ".git"), []byte("gitdir: "+filepath.Join(dest, ".git")), 0600)
	if rm.AddLocal(other) == nil {
		t.Fatal("accepted gitfile")
	}
}
func TestAddLocalRejectsBareAndWrongTopLevel(t *testing.T) {
	for _, flag := range []string{"core.bare", "core.worktree"} {
		t.Run(flag, func(t *testing.T) {
			layout := newTestLayout(t)
			dest := localCheckout(t, "https://github.com/owner/repo.git")
			value := "true"
			if flag == "core.worktree" {
				value = t.TempDir()
			}
			runGit(t, dest, "config", flag, value)
			g := &offlineGit{t: t, real: runner.NewExecRunner()}
			if NewRepoManager(layout, g, nil).AddLocal(dest) == nil {
				t.Fatal("accepted", flag)
			}
		})
	}
}
func TestAddLocalDisappearsBeforeTransactionNeverClones(t *testing.T) {
	layout := newTestLayout(t)
	dest := localCheckout(t, "https://github.com/owner/repo.git")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	removed := false
	g.before = func(args []string) {
		if !removed && args[2] == "remote" {
			removed = true
			os.Rename(dest, dest+"-moved")
		}
	}
	if NewRepoManager(layout, g, nil).AddLocal(dest) == nil {
		t.Fatal("accepted disappearing checkout")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("recreated checkout")
	}
}
func TestAddLocalRejectsChangedOriginBeforeCommit(t *testing.T) {
	layout := newTestLayout(t)
	dest := localCheckout(t, "https://github.com/owner/repo.git")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	reads := 0
	g.before = func(args []string) {
		if args[2] == "remote" {
			reads++
			if reads == 3 {
				runGit(t, dest, "remote", "set-url", "origin", "https://github.com/other/changed.git")
			}
		}
	}
	if NewRepoManager(layout, g, nil).AddLocal(dest) == nil {
		t.Fatal("accepted changed origin")
	}
	entries, _ := registry.ReadEntries(layout.RepositoriesPath())
	if len(entries) != 0 {
		t.Fatal("registered changed origin")
	}
}

func TestAddLocalRejectsPathCollisionAfterOriginRename(t *testing.T) {
	layout := newTestLayout(t)
	dest := localCheckout(t, "https://github.com/owner/first.git")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	rm := NewRepoManager(layout, g, nil)
	if err := rm.AddLocal(dest); err != nil {
		t.Fatal(err)
	}
	runGit(t, dest, "remote", "set-url", "origin", "https://github.com/owner/second.git")
	if err := rm.AddLocal(dest); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("path collision: %v", err)
	}
}
func TestLocalTransactionMissingNeverFallsBackToClone(t *testing.T) {
	layout := newTestLayout(t)
	dest := filepath.Join(t.TempDir(), "missing")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	if err := NewRepoManager(layout, g, nil).addAt("https://github.com/owner/repo.git", dest, true); err == nil || !strings.Contains(err.Error(), "never clones") {
		t.Fatalf("missing error: %v", err)
	}
	if len(g.calls) != 0 {
		t.Fatal("executed git")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("created checkout")
	}
}
func TestAddLocalDoesNotRestoreConcurrentRemoval(t *testing.T) {
	layout := newTestLayout(t)
	dest := localCheckout(t, "https://github.com/owner/repo.git")
	g := &offlineGit{t: t, real: runner.NewExecRunner()}
	rm := NewRepoManager(layout, g, nil)
	if err := rm.AddLocal(dest); err != nil {
		t.Fatal(err)
	}
	reads := 0
	g.before = func(args []string) {
		if args[2] == "remote" {
			reads++
			if reads == 2 {
				if err := registry.UpdateEntries(layout.RepositoriesPath(), func(_ []registry.Entry) ([]registry.Entry, error) { return []registry.Entry{}, nil }); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := rm.AddLocal(dest); err == nil || !strings.Contains(err.Error(), "removed during add") {
		t.Fatalf("concurrent removal: %v", err)
	}
	entries, _ := registry.ReadEntries(layout.RepositoriesPath())
	if len(entries) != 0 {
		t.Fatal("restored removed registration")
	}
}
