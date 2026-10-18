package repo

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

func TestAdd_ConfiguredRootPersistsAcrossSettingChanges(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	root := filepath.Join(t.TempDir(), "Workspace Shared", "repos")
	if err := settings.Write(layout, settings.Config{RepoRoot: root}); err != nil {
		t.Fatal(err)
	}
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, runner.NewExecRunner(), fakeHeads(fake))
	if err := rm.Add(remote); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, remoteName(remote))
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatal(err)
	}
	entries, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil || len(entries) != 1 || entries[0].Path != dest {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if err := settings.Write(layout, settings.Config{RepoRoot: filepath.Join(t.TempDir(), "new-root")}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("local changes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rm.Add(remote); err != nil {
		t.Fatal(err)
	}
	got, err := CheckoutPath(layout, remoteName(remote))
	if err != nil || got != dest {
		t.Fatalf("CheckoutPath=%q, %v", got, err)
	}
	infos, err := rm.List()
	if err != nil || len(infos) != 1 || infos[0].Path != dest {
		t.Fatalf("List=%+v, %v", infos, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Home(), remoteName(remote))); !os.IsNotExist(err) {
		t.Fatalf("unexpected HOME clone: %v", err)
	}
	if _, err := rm.Remove(remoteName(remote)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "keep.txt")); err != nil {
		t.Fatal("remove deleted checkout:", err)
	}
}

func TestAddAt_OverrideNeverRemapsExistingRegistration(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	root := filepath.Join(t.TempDir(), "default")
	if err := settings.Write(layout, settings.Config{RepoRoot: root}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "custom checkout % $ ' ü")
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, runner.NewExecRunner(), fakeHeads(fake))
	if err := rm.AddAt(remote, dest); err != nil {
		t.Fatal(err)
	}
	if err := rm.AddAt(remote, dest); err != nil {
		t.Fatal(err)
	}
	if err := rm.Add(remote); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(layout.RepositoriesPath())
	fake.Calls = nil
	other := filepath.Join(t.TempDir(), "different")
	if err := rm.AddAt(remote, other); err == nil || !strings.Contains(err.Error(), "refusing to move or remap") {
		t.Fatalf("remap error=%v", err)
	}
	after, _ := os.ReadFile(layout.RepositoriesPath())
	if string(before) != string(after) || len(fake.Calls) != 0 {
		t.Fatal("remap changed registry or heads")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("remap created destination: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("override created default root: %v", err)
	}
}

func TestAdd_LegacyNamesNeverFollowConfiguredRoot(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	runGit(t, "", "clone", "-q", remote, dest)
	if err := registry.Write(layout.RepositoriesPath(), []string{name}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Write(layout, settings.Config{RepoRoot: filepath.Join(t.TempDir(), "new")}); err != nil {
		t.Fatal(err)
	}
	rm := NewRepoManager(layout, runner.NewExecRunner(), nil)
	if err := rm.Add(remote); err != nil {
		t.Fatal(err)
	}
	got, err := CheckoutPath(layout, name)
	if err != nil || got != dest {
		t.Fatalf("CheckoutPath=%q, %v", got, err)
	}
	entries, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil || len(entries) != 1 || entries[0].Path != "" {
		t.Fatalf("legacy record changed: %+v, %v", entries, err)
	}
}

func TestAddAt_RejectsSymlinkAboveHomeAndOutsideHome(t *testing.T) {
	for _, kind := range []string{"above-home", "outside-home"} {
		t.Run(kind, func(t *testing.T) {
			layout := newTestLayout(t)
			remote := newBareRemote(t)
			real := t.TempDir()
			base := t.TempDir()
			link := filepath.Join(base, "alias")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(link, "nested", "checkout")
			if kind == "above-home" {
				// The symlink is an ancestor of HOME, not HOME itself.
				t.Setenv("HOME", filepath.Join(link, "home"))
				var err error
				layout, err = paths.New("")
				if err != nil {
					t.Fatal(err)
				}
				dest = filepath.Join(layout.Home(), remoteName(remote))
			}
			rm := NewRepoManager(layout, runner.NewExecRunner(), nil)
			if err := rm.AddAt(remote, dest); err == nil || !strings.Contains(err.Error(), "symlinked component") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAddAt_RejectsInvalidDestinationsBeforeGit(t *testing.T) {
	for _, dest := range []string{"relative/repo", "/", "/tmp/a/../b", "/tmp/with\nnewline", "/tmp/with\x00nul", "/tmp/invalid-\xff"} {
		t.Run(strings.ReplaceAll(dest, "/", "_"), func(t *testing.T) {
			layout := newTestLayout(t)
			git := runner.NewFakeRunner()
			err := NewRepoManager(layout, git, nil).AddAt("https://github.com/example/repo.git", dest)
			if err == nil || len(git.Calls) != 0 {
				t.Fatalf("error=%v calls=%v", err, git.Calls)
			}
		})
	}
}

func TestList_PruneRetainsOtherAbsolutePaths(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	kept := filepath.Join(t.TempDir(), "kept checkout")
	runGit(t, "", "clone", "-q", remote, kept)
	gone := filepath.Join(t.TempDir(), "gone")
	entries := []registry.Entry{{Name: "keep", Path: kept}, {Name: "gone", Path: gone}}
	if err := registry.WriteEntries(layout.RepositoriesPath(), entries); err != nil {
		t.Fatal(err)
	}
	fake := newFakeHeadReconciler()
	infos, err := NewRepoManager(layout, runner.NewExecRunner(), fakeHeads(fake)).List()
	if err != nil || len(infos) != 1 || infos[0].Path != kept {
		t.Fatalf("List=%v, %v", infos, err)
	}
	got, err := registry.ReadEntries(layout.RepositoriesPath())
	if err != nil || !reflect.DeepEqual(got, entries[:1]) {
		t.Fatalf("entries=%+v, %v", got, err)
	}
	if !reflect.DeepEqual(fake.Calls, []string{"DisableClaudeUnit:gone", "ClaudeUnitState:keep"}) {
		t.Fatalf("calls=%v", fake.Calls)
	}
}

func TestList_UnavailableRootAndInvalidCheckoutFailClosed(t *testing.T) {
	for _, kind := range []string{"missing-parent", "inaccessible-parent", "invalid-git", "symlink-parent", "git-error"} {
		t.Run(kind, func(t *testing.T) {
			layout := newTestLayout(t)
			root := filepath.Join(t.TempDir(), "root")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(root, "checkout")
			var git runner.Runner = runner.NewExecRunner()
			switch kind {
			case "missing-parent":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
			case "inaccessible-parent":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses permissions")
				}
				if err := os.Chmod(root, 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0700) })
			case "invalid-git":
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				if err := os.Remove(root); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), root); err != nil {
					t.Fatal(err)
				}
			case "git-error":
				if err := os.MkdirAll(filepath.Join(dest, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
				fr := runner.NewFakeRunner()
				fr.SetResponse("git", []string{"-C", dest, "rev-parse", "--path-format=absolute", "--git-common-dir"}, runner.Response{ExitCode: 1, Stderr: "Input/output error"})
				git = fr
			}
			// Include a genuinely vanished entry to prove no partial detach or rewrite.
			entries := []registry.Entry{{Name: "a-gone", Path: filepath.Join(t.TempDir(), "gone")}, {Name: "blocked", Path: dest}}
			if err := registry.WriteEntries(layout.RepositoriesPath(), entries); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(layout.RepositoriesPath())
			fake := newFakeHeadReconciler()
			if _, err := NewRepoManager(layout, git, fakeHeads(fake)).List(); err == nil {
				t.Fatal("expected fail-closed error")
			}
			after, _ := os.ReadFile(layout.RepositoriesPath())
			if string(before) != string(after) || len(fake.Calls) != 0 {
				t.Fatalf("failure mutated registry/heads: %v", fake.Calls)
			}
		})
	}
}

func TestAddAt_RollsBackOnlyPristineNewCloneOnWriteFailure(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(map[bool]string{false: "pristine", true: "dirty"}[dirty], func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root bypasses permissions")
			}
			layout := newTestLayout(t)
			remote := newBareRemote(t)
			dest := filepath.Join(t.TempDir(), "custom")
			if err := os.MkdirAll(layout.ConfigDir(), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(layout.ConfigDir(), 0700) })
			git := &afterCloneRunner{Runner: runner.NewExecRunner(), after: func() {
				if dirty {
					if err := os.WriteFile(filepath.Join(dest, "local.txt"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Chmod(layout.ConfigDir(), 0500); err != nil {
					t.Fatal(err)
				}
			}}
			if err := NewRepoManager(layout, git, nil).AddAt(remote, dest); err == nil {
				t.Fatal("expected write failure")
			}
			_, err := os.Stat(dest)
			if dirty && err != nil {
				t.Fatalf("dirty checkout removed: %v", err)
			}
			if !dirty && !os.IsNotExist(err) {
				t.Fatalf("pristine clone retained: %v", err)
			}
		})
	}
}

type afterCloneRunner struct {
	runner.Runner
	after func()
}

func (r *afterCloneRunner) Run(name string, args ...string) (string, string, int, error) {
	out, stderr, code, err := r.Runner.Run(name, args...)
	if name == "git" && len(args) > 0 && args[0] == "clone" && err == nil && code == 0 {
		r.after()
	}
	return out, stderr, code, err
}
