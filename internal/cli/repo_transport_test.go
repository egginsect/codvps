package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/registry"
	"github.com/egginsect/codvps/internal/runner"
	"github.com/egginsect/codvps/internal/settings"
)

func TestRepoCloneURLUsesGitHubHTTPSPreference(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"git@github.com:OWNER/REPO.git", "https://github.com/OWNER/REPO.git"},
		{"ssh://git@github.com/OWNER/REPO.git", "https://github.com/OWNER/REPO.git"},
		{"git@GitHub.COM:owner-name/repo_name.v2", "https://github.com/owner-name/repo_name.v2"},
		{"ssh://git@GITHUB.COM/a/r.git/", "https://github.com/a/r.git/"},
		{"git@github.com:a/r", "https://github.com/a/r"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			r := runner.NewFakeRunner()
			r.SetResponse("gh", []string{"config", "get", "git_protocol", "--host", "github.com"}, runner.Response{Stdout: "https\n"})
			if got := repoCloneURL(tc.raw, r); got != tc.want {
				t.Fatalf("repoCloneURL = %q; want %q", got, tc.want)
			}
			want := []runner.Call{{Name: "gh", Args: []string{"config", "get", "git_protocol", "--host", "github.com"}}}
			if !reflect.DeepEqual(r.Calls, want) {
				t.Fatalf("calls = %v; want only %v", r.Calls, want)
			}
		})
	}
}

func TestRepoCloneURLRetainsInputWithoutHTTPSPreference(t *testing.T) {
	for _, tc := range []struct {
		name string
		resp runner.Response
	}{
		{"ssh", runner.Response{Stdout: "ssh\n"}},
		{"empty", runner.Response{}},
		{"unknown", runner.Response{Stdout: "git\n"}},
		{"malformed", runner.Response{Stdout: "https\nssh\n"}},
		{"missing", runner.Response{Err: exec.ErrNotFound}},
		{"nonzero", runner.Response{Stdout: "https\n", ExitCode: 1}},
		{"error", runner.Response{Stdout: "https\n", Err: errors.New("probe failed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runner.NewFakeRunner()
			r.SetResponse("gh", []string{"config", "get", "git_protocol", "--host", "github.com"}, tc.resp)
			const raw = "git@github.com:OWNER/REPO.git"
			if got := repoCloneURL(raw, r); got != raw {
				t.Fatalf("repoCloneURL = %q; want unchanged %q", got, raw)
			}
			if len(r.Calls) != 1 {
				t.Fatalf("calls = %v; want one read-only probe", r.Calls)
			}
		})
	}
}

func TestRepoCloneURLLeavesOtherInputsUnprobed(t *testing.T) {
	for _, raw := range []string{
		"", "OWNER/REPO", "/tmp/REPO", "./git@github.com:OWNER/REPO.git",
		"file:///tmp/REPO.git", "https://github.com/OWNER/REPO.git",
		"http://github.com/OWNER/REPO.git", "git://github.com/OWNER/REPO.git",
		"https://example.com/git@github.com:OWNER/REPO.git",
		"git@github.example.com:OWNER/REPO.git", "git@github.com.example.com:OWNER/REPO.git",
		"git@github.com.:OWNER/REPO.git", "git@github-alias:OWNER/REPO.git",
		"git@ssh.github.com:OWNER/REPO.git", "ssh://git@ssh.github.com:443/OWNER/REPO.git",
		"alice@github.com:OWNER/REPO.git", "github.com:OWNER/REPO.git",
		"ssh://alice@github.com/OWNER/REPO.git", "ssh://github.com/OWNER/REPO.git",
		"ssh://git:password@github.com/OWNER/REPO.git", "ssh://git:@github.com/OWNER/REPO.git",
		"ssh://git@github.com:22/OWNER/REPO.git", "ssh://git@github.com:2222/OWNER/REPO.git",
		"ssh://git@github.com:/OWNER/REPO.git", "ssh://git@github.com:bad/OWNER/REPO.git",
		"ssh://git@github.com/OWNER/REPO.git?", "ssh://git@github.com/OWNER/REPO.git?x=1",
		"ssh://git@github.com/OWNER/REPO.git#", "ssh://git@github.com/OWNER/REPO.git#main",
		"git@github.com:OWNER/REPO.git?x=1", "git@github.com:OWNER/REPO.git#main",
		"git@github.com:/OWNER/REPO.git", "git@github.com:OWNER//REPO.git",
		"git@github.com:OWNER/REPO.git//", "ssh://git@github.com//OWNER/REPO.git",
		"git@github.com:OWNER/../REPO.git", "git@github.com:../REPO.git",
		"git@github.com:OWNER/.", "git@github.com:OWNER/..", "git@github.com:OWNER/.git",
		"git@github.com:OWNER/..git", "git@github.com:OWNER/...git",
		"git@github.com:OWNER/REPO/extra", "git@github.com:OWNER/", "git@github.com:/REPO.git",
		"ssh://git@github.com/OWNER/%52EPO.git", "ssh://git@github.com/OWNER/REPO%2fgit",
		"ssh://git@github.com/OWNER/REPO%00.git", "ssh://git@github.com/OWNER/REPO%xx.git",
		"ssh://%67it@github.com/OWNER/REPO.git", "ssh://git@github.com/OWNER\\REPO.git",
		" git@github.com:OWNER/REPO.git", "git@github.com:OWNER/REPO.git ",
		"git@github.com:OWNER/REPO.git\n", "git@github.com:OWNER/repo name.git",
		"git@github.com:OWNER/repo\x00.git", "git@github.com:OWNER/repo\xff.git",
	} {
		t.Run(raw, func(t *testing.T) {
			r := runner.NewFakeRunner()
			r.SetResponse("gh", []string{"config", "get", "git_protocol", "--host", "github.com"}, runner.Response{Stdout: "https\n"})
			if got := repoCloneURL(raw, r); got != raw {
				t.Fatalf("repoCloneURL = %q; want unchanged %q", got, raw)
			}
			if len(r.Calls) != 0 {
				t.Fatalf("unrelated input invoked gh: %v", r.Calls)
			}
		})
	}
}

// Exercise the production command wiring with local executable fixtures. Git
// records its arguments and fails before any clone, so this never uses network
// access, credential files or the real gh/git binaries.
func TestRepoAddUsesConfiguredGitHubTransportEnvironment(t *testing.T) {
	for _, mode := range []string{"manual", "systemd"} {
		for _, configured := range []bool{false, true} {
			name := mode + "/inherited"
			if configured {
				name = mode + "/configured"
			}
			t.Run(name, func(t *testing.T) {
				layout := manualLayout(t)
				cfg := settings.Config{SessionMode: mode}
				ghDir, gitConfig := "/inherited/github", "/inherited/gitconfig"
				t.Setenv("GH_CONFIG_DIR", ghDir)
				t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
				if configured {
					ghDir, gitConfig = "/configured/github", "/configured/gitconfig"
					cfg.GitHubConfigDir, cfg.GitConfigGlobal = ghDir, gitConfig
				}
				if err := settings.Write(layout, cfg); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				ghLog, gitLog := filepath.Join(bin, "gh-log"), filepath.Join(bin, "git-log")
				t.Setenv("CODVPS_TRANSPORT_GH_LOG", ghLog)
				t.Setenv("CODVPS_TRANSPORT_GIT_LOG", gitLog)
				for binary, script := range map[string]string{
					"gh":  "#!/bin/sh\nprintf '%s\\n' \"$GH_CONFIG_DIR\" \"$GIT_CONFIG_GLOBAL\" \"$@\" > \"$CODVPS_TRANSPORT_GH_LOG\"\nprintf '%s\\n' https\n",
					"git": "#!/bin/sh\nprintf '%s\\n' \"$GH_CONFIG_DIR\" \"$GIT_CONFIG_GLOBAL\" \"$GIT_TERMINAL_PROMPT\" \"$@\" > \"$CODVPS_TRANSPORT_GIT_LOG\"\nprintf '%s\\n' 'fixture stopped before clone' >&2\nexit 1\n",
				} {
					if err := os.WriteFile(filepath.Join(bin, binary), []byte(script), 0700); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("PATH", bin)
				dest := filepath.Join(t.TempDir(), "checkout with spaces")
				err := repoAddAt("git@github.com:OWNER/REPO.git", dest)
				if err == nil || !strings.Contains(err.Error(), "fixture stopped before clone") {
					t.Fatalf("error = %v; want fixture clone error", err)
				}
				for log, want := range map[string]string{
					ghLog:  strings.Join([]string{ghDir, gitConfig, "config", "get", "git_protocol", "--host", "github.com", ""}, "\n"),
					gitLog: strings.Join([]string{ghDir, gitConfig, "0", "clone", "--", "https://github.com/OWNER/REPO.git", dest, ""}, "\n"),
				} {
					got, err := os.ReadFile(log)
					if err != nil || string(got) != want {
						t.Fatalf("%s = %q, %v; want %q", filepath.Base(log), got, err, want)
					}
				}
			})
		}
	}
}

func TestRepoAddReattachesViaHTTPSWithoutChangingOrigin(t *testing.T) {
	const sshURL = "git@github.com:OWNER/REPO.git"
	const httpsURL = "https://github.com/OWNER/REPO.git"
	for _, origin := range []string{sshURL, httpsURL} {
		t.Run(origin, func(t *testing.T) {
			layout := manualLayout(t)
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "unused-gitconfig"))
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "existing checkout")
			r := runner.NewExecRunner()
			for _, args := range [][]string{{"init", dest}, {"-C", dest, "remote", "add", "origin", origin}} {
				if _, stderr, code, err := r.Run(realGit, args...); err != nil || code != 0 {
					t.Fatalf("local Git fixture: %v: %s", err, stderr)
				}
			}
			bin := t.TempDir()
			log := filepath.Join(bin, "ls-remote-log")
			t.Setenv("CODVPS_TRANSPORT_REAL_GIT", realGit)
			t.Setenv("CODVPS_TRANSPORT_EXPECTED", httpsURL)
			t.Setenv("CODVPS_TRANSPORT_GIT_LOG", log)
			// Only local read-only Git commands may reach real Git. Every
			// reachability check is intercepted, so no remote is contacted.
			gitScript := `#!/bin/sh
if [ "$1" = ls-remote ]; then
  [ "$#" = 3 ] && [ "$2" = -- ] && [ "$3" = "$CODVPS_TRANSPORT_EXPECTED" ] || exit 91
  printf '%s\n' "$3" >> "$CODVPS_TRANSPORT_GIT_LOG"
  exit 0
fi
if [ "$1" = -C ] && [ "$3" = rev-parse ]; then
  exec "$CODVPS_TRANSPORT_REAL_GIT" "$@"
fi
if [ "$1" = -C ] && [ "$3" = remote ] && [ "$4" = get-url ]; then
  exec "$CODVPS_TRANSPORT_REAL_GIT" "$@"
fi
printf '%s\n' 'unexpected Git command' >&2
exit 92
`
			for binary, script := range map[string]string{
				"gh":  "#!/bin/sh\n[ \"$*\" = 'config get git_protocol --host github.com' ] || exit 93\nprintf '%s\\n' https\n",
				"git": gitScript,
			} {
				if err := os.WriteFile(filepath.Join(bin, binary), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			for _, destination := range []string{dest, ""} {
				if err := repoAddAt(sshURL, destination); err != nil {
					t.Fatal(err)
				}
			}
			if got, _, code, err := r.Run(realGit, "-C", dest, "remote", "get-url", "origin"); err != nil || code != 0 || strings.TrimSpace(got) != origin {
				t.Fatalf("origin = %q, %v; want unchanged %q", got, err, origin)
			}
			entries, err := registry.ReadEntries(layout.RepositoriesPath())
			want := []registry.Entry{{Name: "REPO", Path: dest}}
			if err != nil || !reflect.DeepEqual(entries, want) {
				t.Fatalf("entries = %v, %v; want %v", entries, err, want)
			}
			if got, err := os.ReadFile(log); err != nil || string(got) != httpsURL+"\n"+httpsURL+"\n" {
				t.Fatalf("reachability checks = %q, %v", got, err)
			}
		})
	}
}
