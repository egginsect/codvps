package cli

import (
	"errors"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	cmd := &versionCmd{}
	err := cmd.Execute([]string{})
	if err != nil {
		t.Errorf("version command failed: %v", err)
	}
}

func TestHelpCommand(t *testing.T) {
	cmd := &helpCmd{}
	err := cmd.Execute([]string{})
	if err != nil {
		t.Errorf("help command failed: %v", err)
	}
}

func TestHelpWithArgs(t *testing.T) {
	cmd := &helpCmd{}
	err := cmd.Execute([]string{"extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("help with args should return InvalidUsageError")
	}
}

// login claude/codex/github are implemented, not stubs. Calling
// them here would reach login.NewContext(), which uses the real Runner and
// real stdin/stdout — invoking the real claude/codex/gh binaries (or
// hanging on a prompt) is exactly what this suite must never do. The full
// behavior is covered hermetically, against fake executables and a temp
// HOME, by internal/login's own tests. This layer only checks argument
// handling that returns before reaching internal/login.
func TestLoginClaudeTooManyArgs(t *testing.T) {
	cmd := &loginCmd{}
	err := cmd.Execute([]string{"claude", "extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("login claude with extra args should return InvalidUsageError, got %T", err)
	}
}

func TestLoginCodexTooManyArgs(t *testing.T) {
	cmd := &loginCmd{}
	err := cmd.Execute([]string{"codex", "extra"})
	if _, ok := err.(*InvalidUsageError); !ok {
		t.Errorf("login codex with extra args should return InvalidUsageError, got %T", err)
	}
}

func TestLoginGithubHelpFlag(t *testing.T) {
	cmd := &loginCmd{}
	err := cmd.Execute([]string{"github", "--help"})
	if err != nil {
		t.Errorf("login github --help should not error, got %v", err)
	}
}

// HOME is cleared so these dispatch-level tests fail at paths.New("")
// before the production wiring ever reaches a real git/systemctl
// subprocess; see the equivalent note in dispatch_test.go. Business logic
// is covered hermetically in internal/repo.
func TestRepoAddValid(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &repoCmd{}
	err := cmd.Execute([]string{"add", "https://example.com"})
	if err == nil {
		t.Errorf("repo add expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("repo add should no longer be not implemented")
	}
}

func TestRepoListValid(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &repoCmd{}
	err := cmd.Execute([]string{"list"})
	if err == nil {
		t.Errorf("repo list expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("repo list should no longer be not implemented")
	}
}

// head enable/disable/list claude and status are implemented. HOME is
// cleared so these dispatch-level tests fail at paths.New("") before the
// production wiring reaches a real systemctl; the behavior is covered
// hermetically in internal/head and by TestHeadSetEnabledGating below.
func TestHeadEnableValid(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &headCmd{}
	err := cmd.Execute([]string{"enable", "claude"})
	if err == nil {
		t.Fatalf("head enable claude expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("head enable claude should no longer be not implemented")
	}
}

func TestHeadDisableAndListClaudeImplemented(t *testing.T) {
	t.Setenv("HOME", "")
	for _, args := range [][]string{{"disable", "claude"}, {"list"}} {
		err := (&headCmd{}).Execute(args)
		if err == nil {
			t.Fatalf("head %v expected an error (HOME unset)", args)
		}
		if _, ok := err.(*NotImplementedError); ok {
			t.Errorf("head %v should no longer be not implemented", args)
		}
	}
}

// The Codex head commands are implemented. With HOME unset they
// fail resolving paths, before any subprocess runs.
func TestHeadCodexCommandsImplemented(t *testing.T) {
	t.Setenv("HOME", "")
	for _, args := range [][]string{{"enable", "codex"}, {"disable", "codex"}, {"pair", "codex"}, {"ensure", "codex"}} {
		err := (&headCmd{}).Execute(args)
		if err == nil {
			t.Fatalf("head %v expected an error (HOME unset)", args)
		}
		if _, ok := err.(*NotImplementedError); ok {
			t.Errorf("head %v should no longer be not implemented", args)
		}
	}
	for _, args := range [][]string{{"pair"}, {"pair", "claude"}, {"ensure", "claude"}, {"ensure", "codex", "extra"}} {
		if _, ok := (&headCmd{}).Execute(args).(*InvalidUsageError); !ok {
			t.Errorf("head %v should be an invalid usage", args)
		}
	}
}

// A head names no repository: the reference's removed per-repository
// spellings get the per-provider usage line.
func TestHeadEnableRejectsPerRepositoryArguments(t *testing.T) {
	for _, args := range [][]string{{"enable", "claude", "hello"}, {"disable", "claude", "hello"}, {"enable", "codex", "hello"}} {
		if _, ok := (&headCmd{}).Execute(args).(*InvalidUsageError); !ok {
			t.Errorf("head %v should be an invalid usage", args)
		}
	}
}

func TestRepoAddRejectsRemovedFlagsWithGuidance(t *testing.T) {
	t.Setenv("HOME", "")
	for _, args := range [][]string{{"add", "--head", "none", "file:///x.git"}, {"add", "--trusted", "file:///x.git"}} {
		err := (&repoCmd{}).Execute(args)
		want := "repo add no longer accepts --head or --trusted; heads are host-scoped, so use codvps enable <claude|codex> separately"
		if err == nil || err.Error() != want {
			t.Errorf("repo %v = %v, want %q", args, err, want)
		}
	}
	err := (&repoCmd{}).Execute([]string{"add", "--bogus"})
	if err == nil || err.Error() != "unknown repo add option: --bogus" {
		t.Errorf("repo add --bogus = %v", err)
	}
}

func TestRemovedVerbsGiveMigrationGuidance(t *testing.T) {
	cases := map[string][]string{
		"removed: restart a head with systemctl; codvps update --yes restarts the heads whose CLI changed": {"restart", "codex"},
		"removed: use codvps update [provider]": {"update", "stage"},
	}
	for want, args := range cases {
		err := Execute(args)
		if err == nil || err.Error() != want {
			t.Errorf("%v = %v, want %q", args, err, want)
		}
	}
}

// TestConfigOnlyLinks: config link is the whole config surface; the old
// connect/sync/status/diff/restore/disconnect spellings are usage errors.
func TestConfigOnlyLinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{}, {"connect", "src"}, {"sync"}, {"sync", "--yes"}, {"status"}, {"diff"}, {"restore", "x"}, {"disconnect"}, {"link"}, {"link", "a", "b"}} {
		var usage *InvalidUsageError
		if err := (&configCmd{}).Execute(args); !errors.As(err, &usage) {
			t.Errorf("config %v = %v, want the config link usage", args, err)
		}
	}
}

func TestComponentsListValid(t *testing.T) {
	cmd := &componentsCmd{}
	err := cmd.Execute([]string{"list"})
	// Components list is now implemented and succeeds even with missing registry
	if err != nil {
		t.Errorf("components list should succeed (missing registry is ok), got %v", err)
	}
}

func TestComponentsStatusValid(t *testing.T) {
	// status probes each CLI's --version; keep the probes off real binaries.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cmd := &componentsCmd{}
	err := cmd.Execute([]string{"status"})
	// Components status is now implemented and succeeds even with missing registry
	if err != nil {
		t.Errorf("components status should succeed (missing registry is ok), got %v", err)
	}
}

// Reference scenario test_head_command_surface (dispatch half): repo help
// and repo --help print the same usage, which names host-scoped heads and
// none of the removed per-repository spellings; unknown repo/head
// subcommands are named in the error.
func TestRepoHelpAndUnknownSubcommands(t *testing.T) {
	for _, line := range []string{
		"Usage: codvps repo <command>",
		"  add <url>          Clone or safely reattach a repository and attach it to every enabled head",
		"Heads are host-scoped, not per repository: codvps enable|disable <claude|codex|cursor>.",
		"Every registered repository is attached to every enabled head.",
	} {
		if !strings.Contains(repoUsage, line+"\n") {
			t.Errorf("repo usage is missing %q", line)
		}
	}
	for _, removed := range []string{"claude on", "codex on", "repo enable", "repo disable", "pick", "--trusted"} {
		if strings.Contains(repoUsage, removed) {
			t.Errorf("repo usage still advertises %q", removed)
		}
	}
	for _, args := range [][]string{{"help"}, {"--help"}} {
		if err := (&repoCmd{}).Execute(args); err != nil {
			t.Errorf("repo %v = %v", args, err)
		}
	}
	if err := (&repoCmd{}).Execute([]string{"nonsense"}); err == nil || err.Error() != "unknown repo command: nonsense" {
		t.Errorf("repo nonsense = %v", err)
	}
	if err := (&headCmd{}).Execute([]string{"nonsense"}); err == nil || err.Error() != "unknown head command: nonsense" {
		t.Errorf("head nonsense = %v", err)
	}
}

// Top-level commands (action-first): enable, disable, list, pair, ensure
func TestTopLevelEnableCommand(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &enableCmd{}
	err := cmd.Execute([]string{"claude"})
	if err == nil {
		t.Fatalf("enable claude expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("enable claude should no longer be not implemented")
	}
}

func TestTopLevelDisableCommand(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &disableCmd{}
	err := cmd.Execute([]string{"claude"})
	if err == nil {
		t.Fatalf("disable claude expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("disable claude should no longer be not implemented")
	}
}

func TestTopLevelListCommand(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &listCmd{}
	err := cmd.Execute([]string{})
	if err == nil {
		t.Fatalf("list expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("list should no longer be not implemented")
	}
}

func TestTopLevelPairCommand(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &pairCmd{}
	err := cmd.Execute([]string{"codex"})
	if err == nil {
		t.Fatalf("pair codex expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("pair codex should no longer be not implemented")
	}
}

func TestTopLevelEnsureCommand(t *testing.T) {
	t.Setenv("HOME", "")
	cmd := &ensureCmd{}
	err := cmd.Execute([]string{"codex"})
	if err == nil {
		t.Fatalf("ensure codex expected an error (HOME unset)")
	}
	if _, ok := err.(*NotImplementedError); ok {
		t.Errorf("ensure codex should no longer be not implemented")
	}
}

// Test that old 'head' spellings still work as hidden aliases
func TestHeadAliasesStillWork(t *testing.T) {
	t.Setenv("HOME", "")
	for _, args := range [][]string{{"enable", "claude"}, {"disable", "claude"}, {"list"}} {
		err := (&headCmd{}).Execute(args)
		if err == nil {
			t.Fatalf("head %v expected an error (HOME unset)", args)
		}
		if _, ok := err.(*NotImplementedError); ok {
			t.Errorf("head %v should no longer be not implemented", args)
		}
	}
}
