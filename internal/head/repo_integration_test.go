package head

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/egginsect/codvps/internal/repo"
	"github.com/egginsect/codvps/internal/runner"
)

// gitFixture runs git hermetically (fixed identity, no system or user
// config, no prompts) to build local clone sources; no network is used.
func gitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=codvps-test", "GIT_AUTHOR_EMAIL=codvps-test@example.com",
		"GIT_COMMITTER_NAME=codvps-test", "GIT_COMMITTER_EMAIL=codvps-test@example.com",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// bareRemote creates <tmp>/<name>.git with one commit.
func bareRemote(t *testing.T, name string) string {
	t.Helper()
	src := t.TempDir()
	gitFixture(t, src, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, src, "add", "README.md")
	gitFixture(t, src, "commit", "-q", "-m", "initial")
	bare := filepath.Join(t.TempDir(), name+".git")
	gitFixture(t, src, "clone", "-q", "--bare", src, bare)
	return bare
}

// repoHost wires the production object graph -- RepoManager over the
// systemd reconciler over the real Claude and Codex heads -- with real git
// against local fixtures and the fake managers. Both heads share one
// System runner, as in production.
func repoHost(t *testing.T) (*codexHost, *repo.RepoManager) {
	t.Helper()
	h := newCodexHost(t)
	git := runner.NewExecRunner("GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
	claude, err := NewClaude(Options{Layout: h.layout, Git: git, Systemd: h.sys, Diag: h.diag, Sleep: func(time.Duration) {}})
	if err != nil {
		t.Fatal(err)
	}
	h.claude = claude
	rec := repo.NewSystemdHeadReconciler(h.layout, h.sys, claude, h.codex, nil)
	return h, repo.NewRepoManager(h.layout, git, []repo.Head{repo.ClaudeRepoHead("claude", rec), repo.CodexRepoHead("codex", rec)})
}

// Reference scenario test_repo_add_auto_attaches_to_the_enabled_claude_head:
// with the head on, `repo add` alone attaches (trust + running unit); with
// it off, add only registers; a later enable sweeps up everything.
func TestRepoAdd_AutoAttachesToTheEnabledClaudeHead(t *testing.T) {
	h, rm := repoHost(t)
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("enable with an empty registry: %v", err)
	}

	if err := rm.Add(bareRemote(t, "auto-attach")); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	if !h.trusted("auto-attach") || !h.unitUp("auto-attach") {
		t.Fatalf("repo add did not attach the new repository to the enabled head")
	}

	if err := h.claude.SetEnabled(false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := rm.Add(bareRemote(t, "late-attach")); err != nil {
		t.Fatalf("repo add with the head off: %v", err)
	}
	if h.trusted("late-attach") || !h.unitDown("late-attach") {
		t.Fatalf("repo add attached a head that is switched off")
	}

	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	for _, name := range []string{"auto-attach", "late-attach"} {
		if !h.trusted(name) || !h.unitUp(name) {
			t.Fatalf("enable did not sweep up %s", name)
		}
	}
}

// repo remove detaches the repository from the Claude head (disable, stop)
// and the head's next reconcile leaves it alone.
func TestRepoRemove_DetachesFromTheClaudeHead(t *testing.T) {
	h, rm := repoHost(t)
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := rm.Add(bareRemote(t, "hello")); err != nil {
		t.Fatalf("repo add: %v", err)
	}
	if !h.unitUp("hello") {
		t.Fatalf("hello not attached")
	}
	if _, err := rm.Remove("hello"); err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if !h.unitDown("hello") {
		t.Fatalf("repo remove left the Claude head for hello running")
	}
	if err := h.claude.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if !h.unitDown("hello") {
		t.Fatalf("a removed repository was re-attached")
	}
	if _, err := os.Stat(filepath.Join(h.layout.Home(), "hello", ".git")); err != nil {
		t.Fatalf("repo remove deleted the checkout: %v", err)
	}
}

// A Claude head failure after registration is reported by repo add, but
// the registration stays (a head problem is not a bad add).
func TestRepoAdd_ReportsClaudeHeadFailureButKeepsRegistration(t *testing.T) {
	h, rm := repoHost(t)
	h.login()
	if err := h.claude.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	h.sd.refuse["broken"] = true
	err := rm.Add(bareRemote(t, "broken"))
	if err == nil {
		t.Fatal("repo add hid a Claude head that never came up")
	}
	infos, listErr := rm.List()
	if listErr != nil || len(infos) != 1 || infos[0].Name != "broken" {
		t.Fatalf("registration was rolled back: %v %v", infos, listErr)
	}
}

// Codex Remote is one daemon for ~/.codex, not scoped to registered
// repositories: repo add and repo remove never touch it, whether it
// is off or running, and repo list only reports it.
func TestRepoAddRemove_LeaveTheCodexHeadAlone(t *testing.T) {
	h, rm := repoHost(t)
	h.installCodex(true)
	if err := rm.Add(bareRemote(t, "codex-one")); err != nil {
		t.Fatalf("repo add with Codex off: %v", err)
	}
	if len(h.sys.callsWith("sudo")) != 0 || h.sys.spawns != 0 {
		t.Fatalf("repo add touched a Codex head that is off: %v", joinCalls(h.sys.calls))
	}
	if _, err := os.Lstat(h.layout.CodexRepositoriesPath()); !os.IsNotExist(err) {
		t.Fatalf("repo add wrote a Codex membership manifest: %v", err)
	}

	if err := h.codex.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	daemon, spawns := h.sys.daemonPID, h.sys.spawns
	h.sys.calls = nil
	h.sys.cliCalls = nil
	if err := rm.Add(bareRemote(t, "codex-two")); err != nil {
		t.Fatalf("repo add with Codex on: %v", err)
	}
	result, err := rm.Remove("codex-two")
	if err != nil {
		t.Fatalf("repo remove: %v", err)
	}
	if len(result.Notices) != 0 {
		t.Fatalf("repo remove reported %v", result.Notices)
	}
	if len(h.sys.callsWith("sudo")) != 0 || len(h.sys.cliCalls) != 0 {
		t.Fatalf("repo add/remove touched the running Codex head: %v %v", joinCalls(h.sys.calls), h.sys.cliCalls)
	}
	if !h.sys.unit.active || h.sys.daemonPID != daemon || h.sys.spawns != spawns {
		t.Fatalf("Codex was restarted or stopped: %+v daemon=%d spawns=%d", h.sys.unit, h.sys.daemonPID, h.sys.spawns)
	}
	if _, err := os.Stat(filepath.Join(h.layout.Home(), "codex-two", ".git")); err != nil {
		t.Fatalf("repo remove deleted the checkout: %v", err)
	}
}
