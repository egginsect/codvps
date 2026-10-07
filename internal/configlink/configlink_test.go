package configlink

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func linksTo(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.Readlink(path)
	if err != nil || got != want {
		t.Fatalf("%s links to %q (%v), want %q", path, got, err, want)
	}
}

// setup is a home and a config checkout at ~/agent-config.
func setup(t *testing.T) (home, configDir, checkout string) {
	home = t.TempDir()
	configDir = filepath.Join(home, ".config", "codvps")
	checkout = filepath.Join(home, "agent-config")
	return
}

// TestLinkEveryCase covers each state a home path can be in.
func TestLinkEveryCase(t *testing.T) {
	home, configDir, checkout := setup(t)
	write(t, filepath.Join(checkout, "claude", "CLAUDE.md"), "repo", 0o644)
	write(t, filepath.Join(checkout, "claude", "skills", "a", "SKILL.md"), "skill", 0o644)
	write(t, filepath.Join(checkout, "claude", "same.md"), "same", 0o644)
	write(t, filepath.Join(checkout, "codex", "AGENTS.md"), "agents", 0o644)
	write(t, filepath.Join(checkout, "claude", "dir.md"), "x", 0o644)
	write(t, filepath.Join(checkout, "claude", "elsewhere.md"), "x", 0o644)
	write(t, filepath.Join(checkout, "README.md"), "not mapped", 0o644)

	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "local edit", 0o644)
	write(t, filepath.Join(home, ".claude", "same.md"), "same", 0o644)
	write(t, filepath.Join(home, ".claude", ".credentials.json"), "secret", 0o600)
	if err := os.MkdirAll(filepath.Join(home, ".claude", "dir.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(home, ".claude", "elsewhere.md")); err != nil {
		t.Fatal(err)
	}

	res, err := Link(home, configDir, checkout)
	if err != nil {
		t.Fatal(err)
	}
	src := func(rel string) string { return filepath.Join(checkout, rel) }
	linksTo(t, filepath.Join(home, ".claude", "CLAUDE.md"), src("claude/CLAUDE.md"))
	linksTo(t, filepath.Join(home, ".claude", "skills", "a", "SKILL.md"), src("claude/skills/a/SKILL.md"))
	linksTo(t, filepath.Join(home, ".claude", "same.md"), src("claude/same.md"))
	linksTo(t, filepath.Join(home, ".codex", "AGENTS.md"), src("codex/AGENTS.md"))

	backup := filepath.Join(configDir, "config-link-backup", ".claude", "CLAUDE.md")
	if res.BackedUp[filepath.Join(home, ".claude", "CLAUDE.md")] != backup || read(t, backup) != "local edit" {
		t.Fatalf("differing home file not backed up: %v", res.BackedUp)
	}
	if len(res.BackedUp) != 1 {
		t.Fatalf("identical file was backed up: %v", res.BackedUp)
	}
	if len(res.Refused) != 2 {
		t.Fatalf("refused = %+v, want the directory and the foreign symlink", res.Refused)
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".claude", "dir.md")); !fi.IsDir() {
		t.Fatal("a refused directory was replaced")
	}
	linksTo(t, filepath.Join(home, ".claude", "elsewhere.md"), "/etc/hostname")
	if read(t, filepath.Join(home, ".claude", ".credentials.json")) != "secret" {
		t.Fatal("credentials next to linked files were touched")
	}
	if _, err := os.Lstat(filepath.Join(home, "README.md")); err == nil {
		t.Fatal("an unmapped repo file was linked")
	}
	if got, _ := Linked(configDir); got != checkout {
		t.Fatalf("Linked = %q, want %q", got, checkout)
	}

	// Editing the home path edits the checkout.
	write(t, filepath.Join(home, ".claude", "CLAUDE.md"), "edited on the host", 0o644)
	if read(t, src("claude/CLAUDE.md")) != "edited on the host" {
		t.Fatal("an edit under home did not reach the checkout")
	}

	// Linking again is a no-op.
	again, err := Link(home, configDir, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Linked) != 0 || len(again.BackedUp) != 0 || len(again.AlreadyLinked) != 4 {
		t.Fatalf("second link changed something: %+v", again)
	}
}

// TestRepoCredentialsAreRefused: credentials and state in the repo are
// never linked.
func TestRepoCredentialsAreRefused(t *testing.T) {
	home, configDir, checkout := setup(t)
	for _, rel := range []string{"claude/.credentials.json", "codex/auth.json", "claude/projects/x/y.jsonl", "codex/sessions/s.json", "cursor/state.sqlite3", "claude/history.jsonl"} {
		write(t, filepath.Join(checkout, rel), "x", 0o600)
	}
	write(t, filepath.Join(checkout, "claude", "CLAUDE.md"), "ok", 0o644)
	res, err := Link(home, configDir, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 6 || len(res.Linked) != 1 {
		t.Fatalf("refused %d, linked %d; want 6 and 1: %+v", len(res.Refused), len(res.Linked), res)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude", ".credentials.json")); err == nil {
		t.Fatal("a repo credential was linked")
	}
}

// TestHomeUnderStateNamedDirIsFine: only paths inside the repo are judged,
// so a home such as /home/user still links.
func TestHomeUnderStateNamedDirIsFine(t *testing.T) {
	home := filepath.Join(t.TempDir(), "projects", "logs")
	checkout := filepath.Join(home, "cfg")
	write(t, filepath.Join(checkout, "claude", "CLAUDE.md"), "ok", 0o644)
	res, err := Link(home, filepath.Join(home, ".config", "codvps"), checkout)
	if err != nil || len(res.Linked) != 1 || len(res.Refused) != 0 {
		t.Fatalf("link under a state-named home: %+v, %v", res, err)
	}
}

// TestUnlinkRestoresRegularFiles replaces links with copies (mode kept) and
// forgets the checkout; unlinking again does nothing.
func TestUnlinkRestoresRegularFiles(t *testing.T) {
	home, configDir, checkout := setup(t)
	write(t, filepath.Join(checkout, "claude", "hook.sh"), "#!/bin/sh\n", 0o755)
	write(t, filepath.Join(checkout, "codex", "AGENTS.md"), "agents", 0o644)
	if _, err := Link(home, configDir, checkout); err != nil {
		t.Fatal(err)
	}
	n, err := Unlink(home, configDir)
	if err != nil || n != 2 {
		t.Fatalf("Unlink = %d, %v; want 2 links replaced", n, err)
	}
	for _, p := range []string{".claude/hook.sh", ".codex/AGENTS.md"} {
		fi, err := os.Lstat(filepath.Join(home, p))
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("%s is not a regular file after unlink", p)
		}
	}
	if fi, _ := os.Stat(filepath.Join(home, ".claude", "hook.sh")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode not kept: %o", fi.Mode().Perm())
	}
	if got, _ := Linked(configDir); got != "" {
		t.Fatalf("checkout still recorded after unlink: %q", got)
	}
	if n, err := Unlink(home, configDir); n != 0 || err != nil {
		t.Fatalf("second unlink = %d, %v", n, err)
	}
}

// TestDriftFindsReplacedAndMissingLinks backs doctor's check.
func TestDriftFindsReplacedAndMissingLinks(t *testing.T) {
	home, configDir, checkout := setup(t)
	write(t, filepath.Join(checkout, "claude", "settings.json"), "{}", 0o644)
	write(t, filepath.Join(checkout, "claude", "CLAUDE.md"), "x", 0o644)
	if _, err := Link(home, configDir, checkout); err != nil {
		t.Fatal(err)
	}
	// A tool rewrote settings.json as a regular file; the repo gained a file.
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	write(t, settings, "{}", 0o644)
	write(t, filepath.Join(checkout, "agents", "new.md"), "x", 0o644)

	replaced, missing, err := Drift(home, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0] != settings {
		t.Fatalf("replaced = %v", replaced)
	}
	if len(missing) != 1 || missing[0] != filepath.Join(home, ".agents", "new.md") {
		t.Fatalf("missing = %v", missing)
	}
}
