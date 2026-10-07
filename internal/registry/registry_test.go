package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid names
		{"lowercase", "myrepo", false},
		{"with numbers", "repo123", false},
		{"with dashes", "my-repo", false},
		{"with dot", "my.repo", false},
		{"with underscore", "my_repo", false},
		{"mixed", "my-repo_v1.2", false},
		{"uppercase", "MyRepo", false},

		// Invalid names
		{"empty", "", true},
		{"starts with dot", ".repo", true},
		{"with space", "my repo", true},
		{"with slash", "my/repo", true},
		{"special char", "my@repo", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"https url", "https://github.com/user/repo", "repo", false},
		{"https with .git", "https://github.com/user/repo.git", "repo", false},
		{"https trailing slash", "https://github.com/user/repo/", "repo", false},
		{"https both", "https://github.com/user/repo.git/", "repo", false},
		{"ssh url", "git@github.com:user/repo", "repo", false},
		{"path", "/home/user/myrepo", "myrepo", false},
		{"path with .git", "/home/user/myrepo.git", "myrepo", false},
		{"path ending in .git only", "/home/user/.git", "user", false},
		{"url starting with dot", "/home/user/.myrepo", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RepoNameFromURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("RepoNameFromURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("RepoNameFromURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCanonicalRemoteIdentity(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		// GitHub HTTPS - should lowercase user/repo
		{"github https", "https://github.com/user/repo", "repository:github.com/user/repo", false},
		{"github https .git", "https://github.com/user/repo.git", "repository:github.com/user/repo", false},
		{"github https trailing /", "https://github.com/user/repo/", "repository:github.com/user/repo", false},
		{"github https both", "https://github.com/user/repo.git/", "repository:github.com/user/repo", false},
		{"github https uppercase", "https://GITHUB.COM/User/Repo", "repository:github.com/user/repo", false},

		// GitHub SSH
		{"github ssh", "git@github.com:user/repo", "repository:github.com/user/repo", false},
		{"github ssh .git", "git@github.com:user/repo.git", "repository:github.com/user/repo", false},
		{"github ssh uppercase", "git@GITHUB.com:User/Repo", "repository:github.com/user/repo", false},

		// GitHub SSH scheme
		{"github ssh scheme", "ssh://git@github.com/user/repo", "repository:github.com/user/repo", false},
		{"github ssh scheme .git", "ssh://git@github.com/user/repo.git", "repository:github.com/user/repo", false},
		{"github ssh scheme trailing /", "ssh://git@github.com/user/repo/", "repository:github.com/user/repo", false},

		// GitHub git scheme
		{"github git scheme", "git://github.com/user/repo", "repository:github.com/user/repo", false},
		{"github git scheme .git", "git://github.com/user/repo.git", "repository:github.com/user/repo", false},
		{"github git scheme uppercase", "git://GITHUB.COM/User/Repo", "repository:github.com/user/repo", false},

		// Non-GitHub HTTPS
		{"gitlab https", "https://gitlab.com/group/project", "repository:gitlab.com/group/project", false},
		{"gitlab https .git", "https://gitlab.com/group/project.git", "repository:gitlab.com/group/project", false},

		// SCP with different user
		{"scp different user", "user@github.com:user/repo", "scp:user@github.com/user/repo", false},
		{"scp other host", "user@example.com:path/repo", "scp:user@example.com/path/repo", false},

		// SSH scheme with non-standard port
		{"ssh custom port", "ssh://user@example.com:2222/path", "network:ssh://user@example.com:2222/path", false},
		{"ssh no port", "ssh://user@example.com/path", "network:ssh://user@example.com/path", false},

		// HTTPS with authentication (note: password not included in output)
		{"https with user:pass", "https://user:password@example.com/path/repo", "network:https://user@example.com/path/repo", false},
		{"https with user", "https://user@example.com/path/repo", "network:https://user@example.com/path/repo", false},

		// HTTP
		{"http", "http://example.com/path/repo", "repository:example.com/path/repo", false},
		{"http with port", "http://example.com:8080/path/repo", "network:http://@example.com:8080/path/repo", false},

		// SCP without user (implicit git user)
		{"scp no user", "example.com:repo", "scp:@example.com/repo", false},

		// Errors
		{"empty url", "", "", true},
		{"whitespace only", "   ", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalRemoteIdentity(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("CanonicalRemoteIdentity() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("CanonicalRemoteIdentity() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWrite(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry")

	names := []string{"repo2", "repo1", "repo3"}
	err := Write(regPath, names)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Read file and verify content
	content, _ := os.ReadFile(regPath)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	expected := []string{"repo1", "repo2", "repo3"}
	if len(lines) != len(expected) {
		t.Errorf("lines count = %d, want %d", len(lines), len(expected))
	}

	for i, line := range lines {
		if line != expected[i] {
			t.Errorf("line %d = %q, want %q", i, line, expected[i])
		}
	}

	// Verify permissions
	stat, _ := os.Stat(regPath)
	if stat.Mode().Perm() != 0600 {
		t.Errorf("permissions = %o, want 0600", stat.Mode().Perm())
	}
}

func TestWriteDeduplication(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry")

	names := []string{"repo1", "repo1", "repo2", "repo1"}
	err := Write(regPath, names)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	content, _ := os.ReadFile(regPath)
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")

	if len(lines) != 2 {
		t.Errorf("after deduplication got %d lines, want 2", len(lines))
	}

	if lines[0] != "repo1" || lines[1] != "repo2" {
		t.Errorf("deduplicated content = %v, want [repo1 repo2]", lines)
	}
}

func TestRead(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry")

	// Create a registry file
	content := "repo1\nrepo2\nrepo3\n"
	if err := os.WriteFile(regPath, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	names, err := Read(regPath)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	expected := []string{"repo1", "repo2", "repo3"}
	if len(names) != len(expected) {
		t.Errorf("length = %d, want %d", len(names), len(expected))
	}

	for i, name := range names {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestReadMissing(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "missing")

	names, err := Read(regPath)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(names) != 0 {
		t.Errorf("expected empty list for missing file, got %v", names)
	}
}

func TestReadEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry")

	if err := os.WriteFile(regPath, []byte(""), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	names, err := Read(regPath)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(names) != 0 {
		t.Errorf("expected empty list for empty file, got %v", names)
	}
}

func TestReadWriteRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	regPath := filepath.Join(tmpDir, "registry")

	// Write
	original := []string{"repo3", "repo1", "repo2"}
	err := Write(regPath, original)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Read
	read, err := Read(regPath)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	expected := []string{"repo1", "repo2", "repo3"}
	if len(read) != len(expected) {
		t.Errorf("length = %d, want %d", len(read), len(expected))
	}

	for i, name := range read {
		if name != expected[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, expected[i])
		}
	}
}
