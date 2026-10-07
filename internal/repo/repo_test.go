package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/paths"
	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
)

// newTestLayout builds a hermetic paths.Layout rooted at a fresh temp HOME,
// with every XDG override cleared so the layout falls back to $HOME-derived
// defaults deterministically regardless of the ambient test environment.
func newTestLayout(t *testing.T) *paths.Layout {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CODVPS_NAMESPACE", "")
	layout, err := paths.New("")
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}
	return layout
}

// runGit runs git in dir (or the process's own working directory when dir
// is empty) with a fully hermetic identity/config, never touching the
// operator's real ~/.gitconfig or prompting for credentials.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=codvps-test",
		"GIT_AUTHOR_EMAIL=codvps-test@example.com",
		"GIT_COMMITTER_NAME=codvps-test",
		"GIT_COMMITTER_EMAIL=codvps-test@example.com",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+t.TempDir(),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newBareRemote creates a local bare git repository with one commit,
// usable as a clone source with no network access.
func newBareRemote(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	runGit(t, src, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, src, "add", "README.md")
	runGit(t, src, "commit", "-q", "-m", "initial")

	bare := filepath.Join(t.TempDir(), "upstream.git")
	runGit(t, "", "clone", "-q", "--bare", src, bare)
	return bare
}

func remoteName(remote string) string {
	return strings.TrimSuffix(filepath.Base(remote), ".git")
}

// --- Add -----------------------------------------------------------------

func TestAdd_ClonesNewRepositoryAndReconcilesHeads(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if err := rm.Add(remote); err != nil {
		t.Fatalf("Add: %v", err)
	}

	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected cloned checkout at %s: %v", dest, err)
	}
	names, err := registry.Read(layout.RepositoriesPath())
	if err != nil {
		t.Fatalf("registry.Read: %v", err)
	}
	if !reflect.DeepEqual(names, []string{name}) {
		t.Fatalf("registry = %v, want [%s]", names, name)
	}
	want := []string{"ReconcileClaude"}
	if !reflect.DeepEqual(fake.Calls, want) {
		t.Fatalf("reconciler calls = %v, want %v", fake.Calls, want)
	}
}

func TestAdd_ReattachesExistingCheckoutWithMatchingRemote(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	runGit(t, "", "clone", "-q", remote, dest)
	// Mark the checkout as already having local state, to prove reattach
	// does not reclone (which would wipe this out).
	if err := os.WriteFile(filepath.Join(dest, "untracked.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if err := rm.Add(remote); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "untracked.txt")); err != nil {
		t.Fatalf("reattach must not disturb the existing checkout: %v", err)
	}
	names, _ := registry.Read(layout.RepositoriesPath())
	if !reflect.DeepEqual(names, []string{name}) {
		t.Fatalf("registry = %v, want [%s]", names, name)
	}
}

func TestAdd_RejectsMismatchedRemote(t *testing.T) {
	layout := newTestLayout(t)
	remoteA := newBareRemote(t)
	remoteB := newBareRemote(t)
	name := remoteName(remoteA)
	dest := filepath.Join(layout.Home(), name)
	runGit(t, "", "clone", "-q", remoteB, dest)

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add(remoteA)
	if err == nil {
		t.Fatalf("expected error for mismatched remote")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want mention of remote mismatch", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("reconciler must not be called on rejection, got %v", fake.Calls)
	}
}

// TestAdd_RejectsUnreachableRemoteOnReattach ports the reference's closing
// `GIT_TERMINAL_PROMPT=0 git ls-remote` reachability check: an existing
// checkout whose origin still canonically matches the requested URL must
// still be rejected if that remote is no longer reachable (here, deleted
// out from under it), rather than being silently reattached.
func TestAdd_RejectsUnreachableRemoteOnReattach(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	runGit(t, "", "clone", "-q", remote, dest)

	if err := os.RemoveAll(remote); err != nil {
		t.Fatalf("delete remote: %v", err)
	}

	git := runner.NewExecRunner("GIT_TERMINAL_PROMPT=0")
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add(remote)
	if err == nil || !strings.Contains(err.Error(), "not reachable without prompting") {
		t.Fatalf("Add() error = %v, want an unreachable-remote rejection", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("reconciler must not be called on rejection, got %v", fake.Calls)
	}
}

func TestAdd_RejectsUnsafeName(t *testing.T) {
	layout := newTestLayout(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add("https://example.com/bad name.git")
	if err == nil {
		t.Fatalf("expected error for unsafe repository name")
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("reconciler must not be called on rejection, got %v", fake.Calls)
	}
}

func TestAdd_RejectsSymlinkedCheckout(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, dest); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add(remote)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Add() error = %v, want symlink rejection", err)
	}
}

func TestAdd_RejectsSymlinkedHomeAncestor(t *testing.T) {
	realHome := t.TempDir()
	symlinkHome := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(realHome, symlinkHome); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	t.Setenv("HOME", symlinkHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("CODVPS_NAMESPACE", "")
	layout, err := paths.New("")
	if err != nil {
		t.Fatalf("paths.New: %v", err)
	}

	remote := newBareRemote(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	addErr := rm.Add(remote)
	if addErr == nil || !strings.Contains(addErr.Error(), "symlinked component") {
		t.Fatalf("Add() error = %v, want symlinked-parent rejection", addErr)
	}
}

func TestAdd_RejectsLinkedWorktree(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)

	primary := filepath.Join(t.TempDir(), "primary")
	runGit(t, "", "clone", "-q", remote, primary)
	runGit(t, primary, "worktree", "add", "-q", "-b", "wt", dest)

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add(remote)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("Add() error = %v, want linked-worktree rejection", err)
	}
}

func TestAdd_RollsBackCleanCloneOnRegistryWriteFailure(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)

	configDir := layout.ConfigDir()
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir configDir: %v", err)
	}
	if err := os.Chmod(configDir, 0o500); err != nil {
		t.Fatalf("chmod configDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(configDir, 0o700) })

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	err := rm.Add(remote)
	if err == nil {
		t.Fatalf("expected registry write failure to surface as an error")
	}
	if _, statErr := os.Lstat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("expected clean clone to be rolled back, dest stat err = %v", statErr)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("reconciler must not run after a rolled-back add, got %v", fake.Calls)
	}
}

// --- List ------------------------------------------------------------------

func TestList_IgnoresStrayCloneNotInRegistry(t *testing.T) {
	layout := newTestLayout(t)
	stray := filepath.Join(layout.Home(), "stray")
	runGit(t, "", "init", "-q", stray)

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	infos, err := rm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("List() = %v, want empty (stray clone must be invisible)", infos)
	}
}

func TestList_PrunesVanishedCheckoutWithoutTouchingCodex(t *testing.T) {
	layout := newTestLayout(t)
	if err := registry.Write(layout.RepositoriesPath(), []string{"gone"}); err != nil {
		t.Fatalf("registry.Write: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	infos, err := rm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("List() = %v, want empty after pruning", infos)
	}
	names, _ := registry.Read(layout.RepositoriesPath())
	if len(names) != 0 {
		t.Fatalf("registry after prune = %v, want empty", names)
	}
	wantCalls := []string{"DisableClaudeUnit:gone"}
	if !reflect.DeepEqual(fake.Calls, wantCalls) {
		t.Fatalf("reconciler calls = %v, want %v (Codex must never be touched by pruning)", fake.Calls, wantCalls)
	}
}

func TestList_FailsClosedOnCorruptRegistry(t *testing.T) {
	layout := newTestLayout(t)
	if err := os.MkdirAll(layout.ConfigDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(layout.RepositoriesPath(), []byte("dup\ndup\n"), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	_, err := rm.List()
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("List() error = %v, want duplicate-registry rejection", err)
	}
}

// TestList_RejectsOversizedRegistry: the registry file is operator-writable
// (and, once corrupted or attacker-controlled, arbitrarily large); List
// must bound its read rather than loading it wholesale.
func TestList_RejectsOversizedRegistry(t *testing.T) {
	layout := newTestLayout(t)
	if err := os.MkdirAll(layout.ConfigDir(), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	oversized := strings.Repeat("a", maxStateFileBytes+1)
	if err := os.WriteFile(layout.RepositoriesPath(), []byte(oversized), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	_, err := rm.List()
	if err == nil || !strings.Contains(err.Error(), "exceeds the maximum allowed size") {
		t.Fatalf("List() error = %v, want an oversized-registry rejection", err)
	}
}

func TestRenderList_ByteExactFormat(t *testing.T) {
	layout := newTestLayout(t)
	remoteA := newBareRemote(t)
	remoteB := newBareRemote(t)
	nameA := remoteName(remoteA) // e.g. "upstream"
	nameB := "zzz-second"

	runGit(t, "", "clone", "-q", remoteA, filepath.Join(layout.Home(), nameA))
	runGit(t, "", "clone", "-q", remoteB, filepath.Join(layout.Home(), nameB))
	if err := registry.Write(layout.RepositoriesPath(), []string{nameA, nameB}); err != nil {
		t.Fatalf("registry.Write: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	fake.ClaudeHeadEnabledVal = true
	fake.CodexEnabledStateVal = "enabled"
	fake.CodexActiveVal = true
	fake.ClaudeUnitStateFunc = func(name string) (bool, bool, error) {
		if name == nameA {
			return true, true, nil // active
		}
		return true, false, nil // enabled only
	}
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	var buf strings.Builder
	if err := rm.RenderList(&buf); err != nil {
		t.Fatalf("RenderList: %v", err)
	}

	pathA := filepath.Join(layout.Home(), nameA)
	pathB := filepath.Join(layout.Home(), nameB)
	want := "Heads: claude=enabled codex=enabled/running\n\n" +
		tableHeader() +
		pad(nameA, 24) + " " + pad(pathA, 40) + " " + "active\n" +
		pad(nameB, 24) + " " + pad(pathB, 40) + " " + "enabled\n"

	if buf.String() != want {
		t.Fatalf("RenderList() =\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestRenderList_EmptyRegistry(t *testing.T) {
	layout := newTestLayout(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	fake.CodexEnabledStateVal = ""
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	var buf strings.Builder
	if err := rm.RenderList(&buf); err != nil {
		t.Fatalf("RenderList: %v", err)
	}
	want := "Heads: claude=disabled codex=unknown/unavailable\n\n" + tableHeader()
	if buf.String() != want {
		t.Fatalf("RenderList() =\n%q\nwant\n%q", buf.String(), want)
	}
}

// pad mirrors the exact "%-Ns" left-justify used by RenderList, used here
// to build the expected string without reusing RenderList's own format
// call (so a format regression cannot mask itself).
func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

func tableHeader() string {
	return pad("REPOSITORY", 24) + " " + pad("PATH", 40) + " " + "CLAUDE_HEAD" + "\n"
}

// --- Remove ----------------------------------------------------------------

func TestRemove_DetachesHeadsAndKeepsCheckout(t *testing.T) {
	layout := newTestLayout(t)
	remote := newBareRemote(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))
	if err := rm.Add(remote); err != nil {
		t.Fatalf("Add: %v", err)
	}
	name := remoteName(remote)
	dest := filepath.Join(layout.Home(), name)
	fake.Calls = nil // isolate Remove's own call trace

	if _, err := rm.Remove(name); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "README.md")); err != nil {
		t.Fatalf("expected checkout to be retained: %v", err)
	}
	names, _ := registry.Read(layout.RepositoriesPath())
	if len(names) != 0 {
		t.Fatalf("registry after remove = %v, want empty", names)
	}
	want := []string{"ClaudeUnitState:" + name, "DisableClaudeUnit:" + name}
	if !reflect.DeepEqual(fake.Calls, want) {
		t.Fatalf("reconciler calls = %v, want %v", fake.Calls, want)
	}
}

func TestRemove_NoOpForUnregisteredNameWhenStateIsSafe(t *testing.T) {
	layout := newTestLayout(t)
	if err := registry.Write(layout.RepositoriesPath(), nil); err != nil {
		t.Fatalf("registry.Write: %v", err)
	}
	if err := os.Chmod(layout.ConfigDir(), 0o700); err != nil {
		t.Fatalf("chmod configDir: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if _, err := rm.Remove("ghost"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want := []string{"ClaudeUnitState:ghost"}
	if !reflect.DeepEqual(fake.Calls, want) {
		t.Fatalf("reconciler calls = %v, want %v (must be a true no-op, never touching Codex)", fake.Calls, want)
	}
}

func TestRemove_FallsThroughWhenStateDirIsUnsafe(t *testing.T) {
	layout := newTestLayout(t)
	if err := registry.Write(layout.RepositoriesPath(), nil); err != nil {
		t.Fatalf("registry.Write: %v", err)
	}
	if err := os.Chmod(layout.ConfigDir(), 0o755); err != nil {
		t.Fatalf("chmod configDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(layout.ConfigDir(), 0o700) })

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if _, err := rm.Remove("ghost"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	found := false
	for _, c := range fake.Calls {
		if c == "DisableClaudeUnit:ghost" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reconciler calls = %v, want DisableClaudeUnit:ghost (unsafe state dir must fall through to the detach path)", fake.Calls)
	}
}

func TestRemove_FallsThroughWhenNameIsRegistered(t *testing.T) {
	layout := newTestLayout(t)
	if err := registry.Write(layout.RepositoriesPath(), []string{"present"}); err != nil {
		t.Fatalf("registry.Write: %v", err)
	}
	if err := os.Chmod(layout.ConfigDir(), 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if _, err := rm.Remove("present"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want := []string{"ClaudeUnitState:present", "DisableClaudeUnit:present"}
	if !reflect.DeepEqual(fake.Calls, want) {
		t.Fatalf("reconciler calls = %v, want %v", fake.Calls, want)
	}
}

func TestRemove_RejectsUnsafeName(t *testing.T) {
	layout := newTestLayout(t)
	git := runner.NewExecRunner()
	fake := newFakeHeadReconciler()
	rm := NewRepoManager(layout, git, fakeHeads(fake))

	if _, err := rm.Remove("../etc"); err == nil {
		t.Fatalf("expected unsafe name to be rejected")
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("reconciler must not be called for a rejected name, got %v", fake.Calls)
	}
}

type errStub string

func (e errStub) Error() string { return string(e) }
