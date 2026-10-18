package repo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

func namedRemote(t *testing.T, name string) string {
	t.Helper()
	remote := newBareRemote(t)
	path := filepath.Join(filepath.Dir(remote), name+".git")
	if err := os.Rename(remote, path); err != nil {
		t.Fatal(err)
	}
	return path
}

// pauseGit blocks one selected Git command after it succeeds. Independent
// registry operations must remain free to finish while the operation waits.
type pauseGit struct {
	runner.Runner
	verb             string
	entered, release chan struct{}
	once             sync.Once
}

func (p *pauseGit) Run(name string, args ...string) (string, string, int, error) {
	out, stderr, code, err := p.Runner.Run(name, args...)
	if name == "git" && len(args) > 0 && args[0] == p.verb && code == 0 && err == nil {
		p.once.Do(func() { close(p.entered); <-p.release })
	}
	return out, stderr, code, err
}
func startPausedAdd(t *testing.T, rm *RepoManager, git *pauseGit, url, dest string) (func(), <-chan error) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- rm.AddAt(url, dest) }()
	var once sync.Once
	resume := func() { once.Do(func() { close(git.release) }) }
	t.Cleanup(resume)
	select {
	case <-git.entered:
	case err := <-result:
		t.Fatalf("add ended before pause: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("add did not reach Git pause")
	}
	return resume, result
}
func waitAdd(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("add remained blocked")
		return nil
	}
}
func newPausedGit(verb string) *pauseGit {
	return &pauseGit{Runner: runner.NewExecRunner(), verb: verb, entered: make(chan struct{}), release: make(chan struct{})}
}

func TestAdd_ConcurrentAdditionAndRemovalAreNotOverwritten(t *testing.T) {
	for _, operation := range []string{"add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			layout := newTestLayout(t)
			alpha, beta := namedRemote(t, "alpha"), namedRemote(t, "beta")
			other := NewRepoManager(layout, runner.NewExecRunner(), nil)
			if operation == "remove" {
				if err := other.Add(beta); err != nil {
					t.Fatal(err)
				}
			}
			git := newPausedGit("clone")
			resume, result := startPausedAdd(t, NewRepoManager(layout, git, nil), git, alpha, "")
			if operation == "add" {
				if err := other.Add(beta); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := other.Remove("beta"); err != nil {
					t.Fatal(err)
				}
			}
			resume()
			if err := waitAdd(t, result); err != nil {
				t.Fatal(err)
			}
			names, err := registry.Read(layout.RepositoriesPath())
			want := "alpha beta"
			if operation == "remove" {
				want = "alpha"
			}
			if err != nil || strings.Join(names, " ") != want {
				t.Fatalf("registry=%v, %v; want %s", names, err, want)
			}
		})
	}
}

func TestAdd_ConcurrentRemovalWinsOverStaleReattach(t *testing.T) {
	layout := newTestLayout(t)
	remote := namedRemote(t, "existing")
	other := NewRepoManager(layout, runner.NewExecRunner(), nil)
	if err := other.Add(remote); err != nil {
		t.Fatal(err)
	}
	git := newPausedGit("ls-remote")
	resume, result := startPausedAdd(t, NewRepoManager(layout, git, nil), git, remote, "")
	if _, err := other.Remove("existing"); err != nil {
		t.Fatal(err)
	}
	resume()
	if err := waitAdd(t, result); err == nil || !strings.Contains(err.Error(), "was removed during add") {
		t.Fatalf("error=%v", err)
	}
	names, err := registry.Read(layout.RepositoriesPath())
	if err != nil || len(names) != 0 {
		t.Fatalf("removed registration resurrected: %v,%v", names, err)
	}
}

func TestAdd_ConcurrentNameConflictDoesNotRemapOrDeleteWinner(t *testing.T) {
	layout := newTestLayout(t)
	remote := namedRemote(t, "conflict")
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	git := newPausedGit("clone")
	resume, result := startPausedAdd(t, NewRepoManager(layout, git, nil), git, remote, first)
	if err := NewRepoManager(layout, runner.NewExecRunner(), nil).AddAt(remote, second); err != nil {
		t.Fatal(err)
	}
	resume()
	if err := waitAdd(t, result); err == nil || !strings.Contains(err.Error(), "changed during add") {
		t.Fatalf("error=%v", err)
	}
	got, err := CheckoutPath(layout, "conflict")
	if err != nil || got != second {
		t.Fatalf("winner changed: %q,%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(second, "README.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("unregistered pristine losing clone retained: %v", err)
	}
}

type detachHookHead struct {
	Head
	hook func()
}

func (h detachHookHead) Detach(name string) error { h.hook(); return h.Head.Detach(name) }

func TestRemove_ConcurrentAdditionIsPreservedAndRemapIsRefused(t *testing.T) {
	for _, operation := range []string{"add", "remap"} {
		t.Run(operation, func(t *testing.T) {
			layout := newTestLayout(t)
			alpha, beta := namedRemote(t, "alpha"), namedRemote(t, "beta")
			other := NewRepoManager(layout, runner.NewExecRunner(), nil)
			if err := other.Add(alpha); err != nil {
				t.Fatal(err)
			}
			newPath := filepath.Join(t.TempDir(), "new-alpha")
			fake := newFakeHeadReconciler()
			head := detachHookHead{Head: ClaudeRepoHead("claude", fake), hook: func() {
				if operation == "add" {
					if err := other.Add(beta); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := registry.UpdateEntries(layout.RepositoriesPath(), func(current []registry.Entry) ([]registry.Entry, error) {
						current[0].Path = newPath
						return current, nil
					}); err != nil {
						t.Fatal(err)
					}
				}
			}}
			_, err := NewRepoManager(layout, runner.NewExecRunner(), []Head{head}).Remove("alpha")
			if operation == "add" {
				if err != nil {
					t.Fatal(err)
				}
				names, err := registry.Read(layout.RepositoriesPath())
				if err != nil || strings.Join(names, " ") != "beta" {
					t.Fatalf("registry=%v,%v", names, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "changed during removal") {
					t.Fatalf("error=%v", err)
				}
				got, err := CheckoutPath(layout, "alpha")
				if err != nil || got != newPath {
					t.Fatalf("remap removed: %q,%v", got, err)
				}
			}
		})
	}
}

type pruneHook func(string) error

func (h pruneHook) DisableClaudeUnit(name string) error { return h(name) }

func TestPrune_ConcurrentChangesRequireRetryAndPreserveRegistration(t *testing.T) {
	for _, operation := range []string{"addition", "reattach", "remap"} {
		t.Run(operation, func(t *testing.T) {
			layout := newTestLayout(t)
			remote := namedRemote(t, "new")
			dest := filepath.Join(layout.Home(), "gone")
			if err := registry.Write(layout.RepositoriesPath(), []string{"gone"}); err != nil {
				t.Fatal(err)
			}
			hook := pruneHook(func(string) error {
				switch operation {
				case "addition":
					return NewRepoManager(layout, runner.NewExecRunner(), nil).Add(remote)
				case "reattach":
					runGit(t, "", "clone", "-q", remote, dest)
				case "remap":
					return registry.UpdateEntries(layout.RepositoriesPath(), func(current []registry.Entry) ([]registry.Entry, error) {
						current[0].Path = filepath.Join(t.TempDir(), "new-gone")
						return current, nil
					})
				}
				return nil
			})
			if _, err := PruneVanishedCheckouts(layout, runner.NewExecRunner(), hook, false); err == nil || !strings.Contains(err.Error(), "retry") {
				t.Fatalf("prune error=%v", err)
			}
			names, err := registry.Read(layout.RepositoriesPath())
			if err != nil || !containsString(names, "gone") || operation == "addition" && !containsString(names, "new") {
				t.Fatalf("concurrent registration lost: %v,%v", names, err)
			}
		})
	}
}

func TestRemove_DetachesKnownNameEvenWhenSnapshotFails(t *testing.T) {
	for _, failure := range []string{"corrupt-registry", "unreadable-registry", "unsafe-lock"} {
		t.Run(failure, func(t *testing.T) {
			layout := newTestLayout(t)
			if err := registry.Write(layout.RepositoriesPath(), []string{"active"}); err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "corrupt-registry":
				if err := os.WriteFile(layout.RepositoriesPath(), []byte("{bad json"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unreadable-registry":
				if os.Geteuid() == 0 {
					t.Skip("root bypasses file permissions")
				}
				if err := os.Chmod(layout.RepositoriesPath(), 0000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(layout.RepositoriesPath(), 0600) })
			case "unsafe-lock":
				if err := os.Remove(layout.RepositoriesPath() + ".lock"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside-lock"), layout.RepositoriesPath()+".lock"); err != nil {
					t.Fatal(err)
				}
			}
			fake := newFakeHeadReconciler()
			fake.ClaudeUnitStateFunc = func(string) (bool, bool, error) { return true, true, nil }
			fake.CursorUnitStateFunc = func(string) (bool, bool, error) { return true, true, nil }
			heads := []Head{ClaudeRepoHead("claude", fake), CursorRepoHead("cursor", fake)}
			if _, err := NewRepoManager(layout, runner.NewExecRunner(), heads).Remove("active"); err == nil {
				t.Fatal("expected registry snapshot error")
			}
			for _, want := range []string{"DisableClaudeUnit:active", "DisableCursorUnit:active"} {
				if !containsString(fake.Calls, want) {
					t.Fatalf("failed snapshot skipped %s: %v", want, fake.Calls)
				}
			}
			if containsString(fake.Calls, "ReconcileClaude") || containsString(fake.Calls, "ReconcileCursor") {
				t.Fatalf("failed removal restarted heads: %v", fake.Calls)
			}
			if failure == "unreadable-registry" {
				if err := os.Chmod(layout.RepositoriesPath(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			data, err := os.ReadFile(layout.RepositoriesPath())
			if err != nil {
				t.Fatal(err)
			}
			want := "active\n"
			if failure == "corrupt-registry" {
				want = "{bad json"
			}
			if string(data) != want {
				t.Fatalf("failed read rewrote registry: %q", data)
			}
		})
	}
}
