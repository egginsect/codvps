package login

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

const authOnlySecret = "ghp_DO_NOT_EXPOSE_CREDENTIAL_CONTENT"

type authOnlyStep struct {
	args     []string
	response runner.Response
	stream   bool
}

// Every allowed invocation is enumerated in order. Any unexpected command,
// including a read of credentials, SSH configuration or installer, fails.
type authOnlyRunner struct {
	t     *testing.T
	steps []authOnlyStep
	calls int
}

func (r *authOnlyRunner) next(name string, args []string, stream bool) runner.Response {
	r.t.Helper()
	if r.calls >= len(r.steps) {
		r.t.Fatalf("unexpected extra command: %s %v", name, args)
	}
	step := r.steps[r.calls]
	r.calls++
	if name != "gh" || !reflect.DeepEqual(args, step.args) || stream != step.stream {
		r.t.Fatalf("command %d = %s %v (stream=%v); want gh %v (stream=%v)", r.calls, name, args, stream, step.args, step.stream)
	}
	return step.response
}

func (r *authOnlyRunner) Run(name string, args ...string) (string, string, int, error) {
	resp := r.next(name, args, false)
	return resp.Stdout, resp.Stderr, resp.ExitCode, resp.Err
}

func (r *authOnlyRunner) RunWithIO(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	resp := r.next(name, args, true)
	data, err := io.ReadAll(stdin)
	if err != nil || len(data) != 0 {
		r.t.Fatalf("login stdin must be empty, got %q, %v", data, err)
	}
	_, _ = io.WriteString(stdout, resp.Stdout)
	_, _ = io.WriteString(stderr, resp.Stderr)
	return resp.ExitCode, resp.Err
}

func authOnlyStatus(ok bool) authOnlyStep {
	resp := runner.Response{Stdout: authOnlySecret, Stderr: authOnlySecret}
	if !ok {
		resp.ExitCode = 1
		resp.Err = errors.New(authOnlySecret)
	}
	return authOnlyStep{args: []string{"auth", "status", "--hostname", "github.com"}, response: resp}
}

func authOnlyAccount() authOnlyStep {
	return authOnlyStep{args: []string{"api", "--hostname", "github.com", "user", "--jq", ".login"}, response: runner.Response{Stdout: "octocat\n", Stderr: authOnlySecret}}
}

func authOnlyProtocol() authOnlyStep {
	return authOnlyStep{args: []string{"config", "set", "git_protocol", "https", "--host", "github.com"}, response: runner.Response{Stdout: authOnlySecret, Stderr: authOnlySecret}}
}

func authOnlyHelper() authOnlyStep {
	return authOnlyStep{args: []string{"auth", "setup-git", "--hostname", "github.com"}, response: runner.Response{Stdout: authOnlySecret, Stderr: authOnlySecret}}
}

func authOnlyLogin() authOnlyStep {
	return authOnlyStep{args: []string{"auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--skip-ssh-key", "--web"}, response: runner.Response{Stderr: "one-time code: ABCD-1234\nhttps://github.com/login/device\n"}, stream: true}
}

func runAuthOnly(t *testing.T, nonInteractive, deferOnly bool, steps []authOnlyStep) (int, string, error) {
	t.Helper()
	r := &authOnlyRunner{t: t, steps: steps}
	var stdout, stderr strings.Builder
	home := filepath.Join(t.TempDir(), "untouched-home")
	ctx := &Context{Runner: r, HomeDir: home, Stdout: &stdout, Stderr: &stderr}
	code, err := GitHubAuthOnly(ctx, nonInteractive, deferOnly)
	if r.calls != len(steps) {
		t.Fatalf("ran %d commands; want %d", r.calls, len(steps))
	}
	out := stdout.String() + stderr.String()
	if strings.Contains(out, authOnlySecret) || (err != nil && strings.Contains(err.Error(), authOnlySecret)) {
		t.Fatalf("subprocess output leaked a credential: output=%q, error=%v", out, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("auth-only touched the filesystem: %v", err)
	}
	return code, out, err
}

func TestGitHubAuthOnlyAuthenticatedConfiguresOnlyHTTPS(t *testing.T) {
	code, out, err := runAuthOnly(t, false, false, []authOnlyStep{authOnlyStatus(true), authOnlyAccount(), authOnlyProtocol(), authOnlyHelper()})
	if code != 0 || err != nil || !strings.Contains(out, "ready as octocat") {
		t.Fatalf("result = %d, %v, %q", code, err, out)
	}
}

func TestGitHubAuthOnlyReadOnlyModes(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		nonInteractive, deferOnly bool
	}{
		{"non-interactive", true, false},
		{"defer", false, true},
		{"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, err := runAuthOnly(t, tc.nonInteractive, tc.deferOnly, []authOnlyStep{authOnlyStatus(true), authOnlyAccount()})
			if code != 0 || err != nil || !strings.Contains(out, "octocat") || !strings.Contains(out, "Read-only") {
				t.Fatalf("result = %d, %v, %q", code, err, out)
			}
		})
	}
}

func TestGitHubAuthOnlyUnauthenticatedReadOnly(t *testing.T) {
	for _, deferOnly := range []bool{false, true} {
		code, out, err := runAuthOnly(t, true, deferOnly, []authOnlyStep{authOnlyStatus(false)})
		if deferOnly {
			if code != 0 || err != nil || !strings.Contains(out, githubAuthOnlyHint) {
				t.Fatalf("preflight result = %d, %v, %q", code, err, out)
			}
		} else if code != 2 || err == nil || !strings.Contains(err.Error(), githubAuthOnlyHint) {
			t.Fatalf("verification result = %d, %v", code, err)
		}
	}
}

func TestGitHubAuthOnlyDeviceLoginUsesHTTPSWithoutSSHScope(t *testing.T) {
	code, out, err := runAuthOnly(t, false, false, []authOnlyStep{authOnlyStatus(false), authOnlyLogin(), authOnlyStatus(true), authOnlyAccount(), authOnlyProtocol(), authOnlyHelper()})
	if code != 0 || err != nil || !strings.Contains(out, "ABCD-1234") || !strings.Contains(out, "ready as octocat") {
		t.Fatalf("result = %d, %v, %q", code, err, out)
	}
}

func TestGitHubAuthOnlyMissingGHNeverInstalls(t *testing.T) {
	for _, deferOnly := range []bool{false, true} {
		status := authOnlyStatus(false)
		status.response.Err = &exec.Error{Name: "gh", Err: exec.ErrNotFound}
		code, out, err := runAuthOnly(t, false, deferOnly, []authOnlyStep{status})
		if deferOnly {
			if code != 0 || err != nil || !strings.Contains(out, "install GitHub CLI") {
				t.Fatalf("preflight result = %d, %v, %q", code, err, out)
			}
		} else if code != 1 || err == nil || !strings.Contains(err.Error(), "install GitHub CLI") {
			t.Fatalf("result = %d, %v", code, err)
		}
	}
}

func TestGitHubAuthOnlyFailureStopsAndSanitizesOutput(t *testing.T) {
	failure := runner.Response{ExitCode: 1, Stdout: authOnlySecret, Stderr: authOnlySecret, Err: errors.New(authOnlySecret)}
	for _, tc := range []struct {
		name  string
		steps []authOnlyStep
	}{
		{"account", []authOnlyStep{authOnlyStatus(true), authOnlyAccount()}},
		{"protocol", []authOnlyStep{authOnlyStatus(true), authOnlyAccount(), authOnlyProtocol()}},
		{"helper", []authOnlyStep{authOnlyStatus(true), authOnlyAccount(), authOnlyProtocol(), authOnlyHelper()}},
		{"login", []authOnlyStep{authOnlyStatus(false), authOnlyLogin()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			last := &tc.steps[len(tc.steps)-1]
			last.response = failure
			if last.stream {
				// gh's interactive device prompt is deliberately streamed;
				// its Go execution error must still never be rendered.
				last.response.Stdout, last.response.Stderr = "", ""
			}
			code, _, err := runAuthOnly(t, false, false, tc.steps)
			if code != 1 || err == nil || !strings.Contains(err.Error(), githubAuthOnlyHint) {
				t.Fatalf("result = %d, %v", code, err)
			}
		})
	}
}

func TestGitHubAuthOnlyVerifiesLoginBeforeConfiguringGit(t *testing.T) {
	code, _, err := runAuthOnly(t, false, false, []authOnlyStep{authOnlyStatus(false), authOnlyLogin(), authOnlyStatus(false)})
	if code != 1 || err == nil || !strings.Contains(err.Error(), "could not be verified") {
		t.Fatalf("result = %d, %v", code, err)
	}
}

func TestGitHubAuthOnlyRejectsUnexpectedAccountOutput(t *testing.T) {
	for _, output := range []string{"", "-bad", "bad-", "bad name", "a\nb", "\x1b[31mred", authOnlySecret, strings.Repeat("a", 40)} {
		account := authOnlyAccount()
		account.response.Stdout = output
		code, _, err := runAuthOnly(t, true, false, []authOnlyStep{authOnlyStatus(true), account})
		if code != 1 || err == nil || !strings.Contains(err.Error(), "invalid account name") {
			t.Fatalf("output %q: result = %d, %v", output, code, err)
		}
	}
}

func TestGitHubAuthOnlyDeferAccountFailureIsReadOnly(t *testing.T) {
	account := authOnlyAccount()
	account.response = runner.Response{ExitCode: 1, Stdout: authOnlySecret, Stderr: authOnlySecret, Err: errors.New(authOnlySecret)}
	code, out, err := runAuthOnly(t, false, true, []authOnlyStep{authOnlyStatus(true), account})
	if code != 0 || err != nil || !strings.Contains(out, "could not verify") {
		t.Fatalf("result = %d, %v, %q", code, err, out)
	}
}
