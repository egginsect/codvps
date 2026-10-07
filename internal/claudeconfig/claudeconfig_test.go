package claudeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, home, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(Path(home), []byte(content), mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readConfig(t *testing.T, home string) string {
	t.Helper()
	data, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(data)
}

func TestSeedWorkspaceTrust_CreatesFileWithReferenceShape(t *testing.T) {
	home := t.TempDir()
	if err := SeedWorkspaceTrust(home, []string{"/home/user/alpha", "/home/user/beta"}); err != nil {
		t.Fatalf("SeedWorkspaceTrust: %v", err)
	}
	want := `{
  "projects": {
    "/home/user/alpha": {
      "hasTrustDialogAccepted": true
    },
    "/home/user/beta": {
      "hasTrustDialogAccepted": true
    }
  }
}
`
	if got := readConfig(t, home); got != want {
		t.Fatalf("~/.claude.json =\n%s\nwant\n%s", got, want)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("new ~/.claude.json mode = %o, want 600", fi.Mode().Perm())
	}
	for _, ws := range []string{"/home/user/alpha", "/home/user/beta"} {
		if ok, err := WorkspaceTrusted(home, ws); err != nil || !ok {
			t.Fatalf("WorkspaceTrusted(%s) = %v, %v", ws, ok, err)
		}
	}
}

// Everything the Claude CLI keeps in ~/.claude.json survives, in its
// original order, and the file mode is preserved.
func TestSeedWorkspaceTrust_PreservesOtherKeysOrderAndMode(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"numStartups": 12, "zeta": {"b": 1, "a": [1, 2.50, "x<y&z"]},
 "projects": {"/home/user/old": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": false}},
 "userID": "abc"}`, 0o640)

	if err := SeedWorkspaceTrust(home, []string{"/home/user/old", "/home/user/new"}); err != nil {
		t.Fatalf("SeedWorkspaceTrust: %v", err)
	}
	want := `{
  "numStartups": 12,
  "zeta": {
    "b": 1,
    "a": [
      1,
      2.50,
      "x<y&z"
    ]
  },
  "projects": {
    "/home/user/old": {
      "allowedTools": [
        "Bash"
      ],
      "hasTrustDialogAccepted": true
    },
    "/home/user/new": {
      "hasTrustDialogAccepted": true
    }
  },
  "userID": "abc"
}
`
	if got := readConfig(t, home); got != want {
		t.Fatalf("~/.claude.json =\n%s\nwant\n%s", got, want)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want the original 640", fi.Mode().Perm())
	}
}

func TestSeedWorkspaceTrust_AlreadyTrustedDoesNotRewrite(t *testing.T) {
	home := t.TempDir()
	original := `{"projects":{"/home/user/a":{"hasTrustDialogAccepted":true}}}`
	writeConfig(t, home, original, 0o600)
	if err := SeedWorkspaceTrust(home, []string{"/home/user/a"}); err != nil {
		t.Fatalf("SeedWorkspaceTrust: %v", err)
	}
	if got := readConfig(t, home); got != original {
		t.Fatalf("an already-trusted workspace rewrote the file: %s", got)
	}
}

func TestSeedWorkspaceTrust_EmptyListIsNoOp(t *testing.T) {
	home := t.TempDir()
	if err := SeedWorkspaceTrust(home, nil); err != nil {
		t.Fatalf("SeedWorkspaceTrust: %v", err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Fatalf("an empty seed created ~/.claude.json: %v", err)
	}
}

func TestSeedWorkspaceTrust_RejectsWrongShapesWithoutWriting(t *testing.T) {
	cases := map[string]string{
		"top-level array":   `[1,2]`,
		"invalid JSON":      `{"projects":`,
		"projects not dict": `{"projects": []}`,
		"entry not dict":    `{"projects": {"/home/user/a": true}}`,
		"trailing data":     `{} {}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			writeConfig(t, home, content, 0o600)
			err := SeedWorkspaceTrust(home, []string{"/home/user/a"})
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
			if !strings.Contains(err.Error(), Path(home)) {
				t.Fatalf("error does not name the file: %v", err)
			}
			if got := readConfig(t, home); got != content {
				t.Fatalf("a rejected config was modified: %s", got)
			}
		})
	}
}

func TestSeedWorkspaceTrust_RejectsRelativePath(t *testing.T) {
	if err := SeedWorkspaceTrust(t.TempDir(), []string{"relative"}); err == nil {
		t.Fatal("expected a relative workspace path to be rejected")
	}
}

func TestSeedWorkspaceTrust_WritesThroughSymlinkAtomically(t *testing.T) {
	home := t.TempDir()
	targetDir := t.TempDir()
	target := filepath.Join(targetDir, "claude.json")
	if err := os.WriteFile(target, []byte(`{"keep":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path(home)); err != nil {
		t.Fatal(err)
	}
	if err := SeedWorkspaceTrust(home, []string{"/home/user/a"}); err != nil {
		t.Fatalf("SeedWorkspaceTrust: %v", err)
	}
	if fi, err := os.Lstat(Path(home)); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the ~/.claude.json symlink was replaced: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"keep": 1`) || !strings.Contains(string(data), `"hasTrustDialogAccepted": true`) {
		t.Fatalf("symlink target not updated correctly: %s", data)
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("atomic write left temp files behind: %v", entries)
	}
}

func TestSeedRemoteConsent_SetsFlagAndPreservesKeys(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, `{"a":1,"remoteDialogSeen":false}`, 0o600)
	if err := SeedRemoteConsent(home); err != nil {
		t.Fatalf("SeedRemoteConsent: %v", err)
	}
	want := "{\n  \"a\": 1,\n  \"remoteDialogSeen\": true\n}\n"
	if got := readConfig(t, home); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWorkspaceTrusted_RequiresLiteralTrue(t *testing.T) {
	home := t.TempDir()
	if ok, err := WorkspaceTrusted(home, "/x"); err != nil || ok {
		t.Fatalf("missing file: got %v, %v", ok, err)
	}
	writeConfig(t, home, `{"projects":{"/x":{"hasTrustDialogAccepted":"true"}}}`, 0o600)
	if ok, err := WorkspaceTrusted(home, "/x"); err != nil || ok {
		t.Fatalf("string \"true\" must not count as trusted: got %v, %v", ok, err)
	}
}

func TestRemoteConsentSeeded(t *testing.T) {
	home := t.TempDir()
	if RemoteConsentSeeded(home) {
		t.Fatal("a missing ~/.claude.json reported consent")
	}
	writeConfig(t, home, `{"remoteDialogSeen":"yes"}`, 0o600)
	if RemoteConsentSeeded(home) {
		t.Fatal("a non-boolean remoteDialogSeen reported consent")
	}
	writeConfig(t, home, `[1]`, 0o600)
	if RemoteConsentSeeded(home) {
		t.Fatal("a non-object config reported consent")
	}
	if err := SeedRemoteConsent(home); err == nil {
		t.Fatal("seeding over a non-object config should fail")
	}
	writeConfig(t, home, `{"a":1}`, 0o600)
	if err := SeedRemoteConsent(home); err != nil {
		t.Fatal(err)
	}
	if !RemoteConsentSeeded(home) {
		t.Fatal("seeded consent was not reported")
	}
}
