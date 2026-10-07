package login

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egginsect/codvps/internal/runner"
)

// testEnv is the hermetic sandbox every test in this package runs in: a
// temp HOME (never the real one), a temp PATH populated only with fake
// gh/ssh/ssh-keygen/git/claude/codex executables (never the real ones —
// see the fakeGH/fakeSSH/fakeSSHKeygen/fakeGit/fakeClaude/fakeCodex
// scripts below), and a temp state dir the fakes use to remember things
// like "already logged in" between calls, the way the reference's own
// STUB_STATE_DIR does.
type testEnv struct {
	t        *testing.T
	Home     string
	StateDir string
	LogDir   string
}

// newTestEnv sets up the sandbox and points PATH/HOME at it for the
// duration of the test (t.Setenv, so it is restored automatically).
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	env := &testEnv{
		t:        t,
		Home:     filepath.Join(root, "home"),
		StateDir: filepath.Join(root, "state"),
		LogDir:   filepath.Join(root, "logs"),
	}
	for _, d := range []string{env.Home, filepath.Join(env.Home, ".ssh"), env.StateDir, env.LogDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", binDir, err)
	}
	for name, script := range map[string]string{
		"gh":         fakeGH,
		"ssh":        fakeSSH,
		"ssh-keygen": fakeSSHKeygen,
		"git":        fakeGit,
		"claude":     fakeClaude,
		"codex":      fakeCodex,
	} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	t.Setenv("HOME", env.Home)
	t.Setenv("STATE_DIR", env.StateDir)
	t.Setenv("LOG_DIR", env.LogDir)
	// /usr/bin and /bin so #!/usr/bin/env bash and the coreutils the fakes
	// use (sha256sum, awk, chmod...) resolve; none of gh/ssh/ssh-keygen/git
	// exist there under the names we shadow because binDir comes first.
	t.Setenv("PATH", binDir+":/usr/bin:/bin")
	// Never let a real GH_TOKEN/GITHUB_TOKEN or GH_CONFIG_DIR from the
	// invoking shell leak into a test.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(env.Home, ".config", "gh"))

	return env
}

// Context builds a login.Context wired to this sandbox: a real
// exec.Command Runner (reaching only the fakes on the sandboxed PATH), a
// non-tty stdin by default, and every write scoped under env.Home.
func (env *testEnv) Context() *Context {
	return &Context{
		Runner:  runner.NewExecRunner(),
		HomeDir: env.Home,
		Stdin:   strings.NewReader(""),
		Stdout:  &strings.Builder{},
		Stderr:  &strings.Builder{},
		IsTTY:   func() bool { return false },
	}
}

// setLoggedIn marks the fake gh account as logged in, and returns the tsv
// path fake gh keeps registered keys in.
func (env *testEnv) setLoggedIn() {
	env.t.Helper()
	if err := os.WriteFile(filepath.Join(env.StateDir, "logged-in"), nil, 0o600); err != nil {
		env.t.Fatalf("mark logged in: %v", err)
	}
}

func (env *testEnv) setMetaFail() {
	env.t.Helper()
	if err := os.WriteFile(filepath.Join(env.StateDir, "meta-fail"), nil, 0o600); err != nil {
		env.t.Fatalf("mark meta-fail: %v", err)
	}
}

func (env *testEnv) setSSHKeyListFail() {
	env.t.Helper()
	if err := os.WriteFile(filepath.Join(env.StateDir, "ssh-key-list-fail"), nil, 0o600); err != nil {
		env.t.Fatalf("mark ssh-key-list-fail: %v", err)
	}
}

func (env *testEnv) setSSHFail() {
	env.t.Helper()
	if err := os.WriteFile(filepath.Join(env.StateDir, "ssh-fail"), nil, 0o600); err != nil {
		env.t.Fatalf("mark ssh-fail: %v", err)
	}
}

// callLog returns each recorded call to the named fake as its argv slice,
// in call order, by reading LogDir/<name>.log (\x1f-separated args, one
// call per line — see the fake scripts' logging preamble).
func (env *testEnv) callLog(t *testing.T, name string) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(env.LogDir, name+".log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s.log: %v", name, err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		calls = append(calls, strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f"))
	}
	return calls
}

// countCalls counts calls to name whose argv starts with prefix.
func (env *testEnv) countCalls(t *testing.T, name string, prefix ...string) int {
	t.Helper()
	n := 0
	for _, call := range env.callLog(t, name) {
		if len(call) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if call[i] != p {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}

// keyPath is the fixed codvps-managed GitHub key path under this sandbox.
func (env *testEnv) keyPath() string {
	return filepath.Join(env.Home, ".ssh", "codvps_github_ed25519")
}

// The fake executables below stand in for gh, ssh, ssh-keygen, git, claude
// and codex. Every one of them only ever runs from a test's sandboxed PATH
// (see newTestEnv); the real binaries are never invoked by this package's
// tests. Each logs its full argv (via LOG_DIR) so tests can assert on exact
// call sequences, and reads/writes only files under STATE_DIR/HOME.

const fakeLogPreamble = `log_call() {
  local out="$LOG_DIR/$1.log"
  shift
  { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"
}
`

const fakeGH = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call gh "$@"

cmd=${1-}
sub=${2-}

case "$cmd" in
  auth)
    case "$sub" in
      status)
        if [[ -n ${GH_TOKEN:-}${GITHUB_TOKEN:-} ]]; then
          login=${GH_FAKE_LOGIN:-token-user}
          scopes=${GH_FAKE_SCOPES:-admin:public_key, repo}
          printf '{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"%s","tokenSource":"GH_TOKEN","scopes":"%s","gitProtocol":"ssh"}]}}\n' "$login" "$scopes"
        elif [[ -e "$STATE_DIR/logged-in" ]]; then
          login=${GH_FAKE_LOGIN:-octocat}
          scopes=${GH_FAKE_SCOPES:-admin:public_key, repo}
          src=${GH_FAKE_TOKEN_SOURCE:-keyring}
          printf '{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"%s","tokenSource":"%s","scopes":"%s","gitProtocol":"ssh"}]}}\n' "$login" "$src" "$scopes"
        else
          printf '{"hosts":{}}\n'
        fi
        exit 0
        ;;
      login)
        # gh's device flow when stdin is not a terminal: code + URL on
        # stderr, then it waits for the code to be entered elsewhere.
        [[ -t 0 ]] && { echo "fake gh: stdin must not be a terminal for the device flow" >&2; exit 1; }
        echo "! First copy your one-time code: ABCD-1234" >&2
        echo "Open this URL to continue in your web browser: https://github.com/login/device" >&2
        : >"$STATE_DIR/logged-in"; exit 0 ;;
      *) exit 1 ;;
    esac
    ;;
  api)
    case "$sub" in
      user)
        shift 2
        jq_expr=
        while (($#)); do
          case "$1" in --jq) jq_expr=$2; shift 2 ;; *) shift ;; esac
        done
        case "$jq_expr" in
          .login) printf '%s\n' "${GH_FAKE_LOGIN:-octocat}" ;;
          '.name // .login') printf '%s\n' "${GH_FAKE_NAME:-${GH_FAKE_LOGIN:-octocat}}" ;;
          .id) printf '%s\n' "${GH_FAKE_ID:-999}" ;;
          *) printf '\n' ;;
        esac
        exit 0
        ;;
      meta)
        if [[ -e "$STATE_DIR/meta-fail" ]]; then
          printf 'error: could not reach api.github.com\n' >&2
          exit 1
        fi
        shift 2
        jq_expr=
        while (($#)); do
          case "$1" in --jq) jq_expr=$2; shift 2 ;; *) shift ;; esac
        done
        case "$jq_expr" in
          '.ssh_keys[]')
            printf '%s\n' 'ssh-ed25519 AAAAFAKEEDKEYDATA'
            printf '%s\n' 'ssh-rsa AAAAFAKERSAKEYDATA'
            ;;
          *) printf '\n' ;;
        esac
        exit 0
        ;;
      *) exit 1 ;;
    esac
    ;;
  ssh-key)
    case "$sub" in
      list)
        if [[ -e "$STATE_DIR/ssh-key-list-fail" ]]; then
          printf 'error: could not list SSH keys\n' >&2
          exit 1
        fi
        cat "$STATE_DIR/ssh-keys.tsv" 2>/dev/null
        exit 0
        ;;
      add)
        shift 2
        key_path=$1
        shift || true
        title=
        while (($#)); do
          case "$1" in --title) title=$2; shift 2 ;; *) shift ;; esac
        done
        key_line=$(cat "$key_path")
        printf '%s\t%s\t2026-01-01T00:00:00Z\t1\tauthentication\n' "$title" "$key_line" >>"$STATE_DIR/ssh-keys.tsv"
        exit 0
        ;;
      *) exit 1 ;;
    esac
    ;;
  *) exit 1 ;;
esac
`

const fakeSSH = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call ssh "$@"

for a in "$@"; do
  if [[ $a == "-G" ]]; then
    cfg="$HOME/.ssh/config"
    if [[ -f $cfg ]]; then
      awk '
        BEGIN { inblock = 0 }
        tolower($1) == "host" {
          inblock = 0
          for (i = 2; i <= NF; i++) if (tolower($i) == "github.com") inblock = 1
          next
        }
        inblock == 1 && tolower($1) == "identityfile" { print "identityfile", $2 }
      ' "$cfg"
    fi
    exit 0
  fi
done

if [[ -e "$STATE_DIR/ssh-fail" ]]; then
  printf 'git@github.com: Permission denied (publickey).\n' >&2
  exit 255
fi
printf "Hi %s! You've successfully authenticated, but GitHub does not provide shell access.\n" \
  "${GH_FAKE_SSH_LOGIN:-${GH_FAKE_LOGIN:-octocat}}" >&2
exit 1
`

const fakeSSHKeygen = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call ssh-keygen "$@"

if [[ ${1-} == "-l" ]]; then
  path=${4-}
  h=$(sha256sum "$path" 2>/dev/null | awk '{print $1}')
  printf '256 SHA256:%s %s (ED25519)\n' "${h:0:43}" "$path"
  exit 0
fi

path=
while (($#)); do
  case "$1" in
    -f) path=$2; shift 2 ;;
    -N|-C|-t) shift 2 ;;
    -q) shift ;;
    *) shift ;;
  esac
done
if [[ -z $path ]]; then
  printf 'ssh-keygen: missing -f\n' >&2
  exit 1
fi
printf 'Generating public/private ed25519 key pair.\n'
printf 'FAKE-PRIVATE-KEY %s\n' "$path" >"$path"
chmod 600 "$path"
suffix=$(printf '%s' "$path" | sha256sum | cut -c1-24)
printf 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI%s codvps-github\n' "$suffix" >"$path.pub"
printf 'Your identification has been saved in %s\n' "$path"
printf 'Your public key has been saved in %s.pub\n' "$path"
printf 'The key fingerprint is:\nSHA256:FAKE0000000000000000000000000000000000000 codvps-github\n'
exit 0
`

const fakeGit = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call git "$@"

if [[ ${1-} == "clone" ]]; then
  printf 'FAKE GIT: clone attempted: %s\n' "$*" >&2
  exit 99
fi

if [[ ${1-} == "config" ]]; then
  shift
  get=0
  args=()
  for a in "$@"; do
    case "$a" in
      --global) : ;;
      --get) get=1 ;;
      *) args+=("$a") ;;
    esac
  done
  key=${args[0]-}
  store="$STATE_DIR/gitconfig-$key"
  if ((${#args[@]} >= 2)); then
    printf '%s' "${args[1]}" >"$store"
    exit 0
  fi
  if [[ $get == 1 && -f $store ]]; then
    cat "$store"
    exit 0
  fi
  exit 1
fi

exit 1
`

const fakeClaude = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call claude "$@"
if [[ -e "$STATE_DIR/claude-fail" ]]; then
  printf 'fake claude: login failed\n' >&2
  exit 1
fi
: >"$STATE_DIR/claude-credential"
printf 'Logged in to Claude.\n'
exit 0
`

const fakeCodex = `#!/usr/bin/env bash
set -u
` + fakeLogPreamble + `
log_call codex "$@"
if [[ -e "$STATE_DIR/codex-fail" ]]; then
  printf 'fake codex: login failed\n' >&2
  exit 1
fi
if [[ -z ${CODEX_HOME:-} ]]; then
  printf 'fake codex: CODEX_HOME not set\n' >&2
  exit 1
fi
mkdir -p "$CODEX_HOME"
if [[ ! -e "$STATE_DIR/codex-no-auth" ]]; then
  printf '{"fake":true}\n' >"$CODEX_HOME/auth.json"
fi
printf 'Logged in to Codex.\n'
exit 0
`
