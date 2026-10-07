package providers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/login"
	"github.com/egginsect/codvps/internal/runner"
)

// loginContext is a hermetic login.Context: every command, including one
// that needs extra environment, goes to fake.
func loginContext(t *testing.T, fake *runner.FakeRunner, gotEnv *[]string) *login.Context {
	t.Helper()
	return &login.Context{
		Runner:  fake,
		HomeDir: t.TempDir(),
		EnvRunner: func(env []string) runner.Runner {
			*gotEnv = env
			return fake
		},
		Stdin:  strings.NewReader(""),
		Stdout: &strings.Builder{},
		Stderr: &strings.Builder{},
		IsTTY:  func() bool { return false },
	}
}

// `codvps login claude` is exactly `claude auth login`, after the
// clipped-URL hint, and seeds Remote Control consent.
func TestClaudeLogin(t *testing.T) {
	fake := runner.NewFakeRunner()
	var env []string
	ctx := loginContext(t, fake, &env)
	if err := login.Run(ctx, All().Lookup("claude").Login.Command); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Name != "claude" || strings.Join(fake.Calls[0].Args, " ") != "auth login" || env != nil {
		t.Fatalf("calls = %+v env = %v", fake.Calls, env)
	}
	if !strings.HasPrefix(ctx.Stdout.(*strings.Builder).String(), "Claude remote login: if the URL is clipped, press c to copy it;") {
		t.Fatal("the login hint was not shown first")
	}
	cfg, err := os.ReadFile(filepath.Join(ctx.HomeDir, ".claude.json"))
	if err != nil || !strings.Contains(string(cfg), `"remoteDialogSeen": true`) {
		t.Fatalf("~/.claude.json = %q (%v)", cfg, err)
	}
}

// `codvps login codex` is exactly `codex login --device-auth` with
// CODEX_HOME scoped to the Codex Remote home, and must leave a non-empty
// auth.json there.
func TestCodexLogin(t *testing.T) {
	fake := runner.NewFakeRunner()
	var env []string
	ctx := loginContext(t, fake, &env)
	cmd := All().Lookup("codex").Login.Command
	auth := filepath.Join(ctx.HomeDir, ".codex", "auth.json")
	err := login.Run(ctx, cmd)
	if err == nil || err.Error() != "codex login completed without creating "+auth {
		t.Fatalf("login without a credential = %v", err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0].Name != "codex" || strings.Join(fake.Calls[0].Args, " ") != "login --device-auth" {
		t.Fatalf("calls = %+v", fake.Calls)
	}
	if strings.Join(env, " ") != "CODEX_HOME="+filepath.Join(ctx.HomeDir, ".codex") {
		t.Fatalf("login env = %v", env)
	}
	if err := os.MkdirAll(filepath.Dir(auth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(auth, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := login.Run(ctx, cmd); err != nil {
		t.Fatalf("login with a credential = %v", err)
	}
}
