#!/usr/bin/env bash
set -euo pipefail

# Test: codvps login claude, head enable|disable|list claude, status, and
# repo add/remove attaching to the Claude head, against the product
# claude-remote@.service / claude-remote.slice templates (installed by the
# Dockerfile from internal/systemd/units) and the fake claude CLI.
#
# Covers (see docs/test-parity.md for the full mapping):
#   - test_components_login_claude_refused_unselected         (tests/components.sh)
#   - test_install_fake_claude_and_cred                        (tests/smoke.sh)
#   - test_login_claude_uses_auth_login                        (tests/smoke.sh)
#   - test_unit_installed                                      (tests/smoke.sh)
#   - test_claude_head_seeds_workspace_trust                   (tests/smoke.sh)
#   - test_head_enable_claude_attaches_every_registered_repo   (tests/smoke.sh)
#   - test_claude_remote_slice_budget                          (tests/smoke.sh)
#   - test_remote_control_invocation                           (tests/smoke.sh)
#   - test_restart_always                                      (tests/smoke.sh)
#   - test_claude_head_lifecycle_is_host_scoped                (tests/smoke.sh)
#   - test_removed_command_spellings_die_with_migration_guidance (tests/smoke.sh)
#   - test_repo_add_auto_attaches_to_the_enabled_claude_head   (tests/smoke.sh)
#   - test_old_commands_show_migration_guidance                (tests/smoke.sh)
#   - test_claude_head_fails_loudly_when_a_workspace_is_untrusted (tests/smoke.sh)
#   - test_claude_is_never_launched_interactively              (tests/smoke.sh)
#
# The untrusted-workspace scenario cannot make seeding a no-op the way the
# reference does (by shadowing python3, which the Go port does not use), so
# it lists the workspace in the fake claude's refuse-workspaces file: Claude
# keeps rejecting that workspace, the head crash-loops, and `head enable`
# must detect it, stop the unit and fail loudly.

WORK="$HOME/tmp/head15"
CACHE="$HOME/.cache/fake-claude"
CRED="$HOME/.claude/.credentials.json"
FLAG="$HOME/.config/codvps/claude-head-enabled"
REGISTRY="$HOME/.config/codvps/repositories"
COMPONENTS=/etc/codvps/components.json
UNIT_FILE=/etc/systemd/user/claude-remote@.service
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

select_components() {
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":[%s],"switch":"none"}\n' "$1" | sudo tee "$COMPONENTS" >/dev/null
  sudo chmod 0644 "$COMPONENTS"
}

cleanup() {
  "$CODVPS_BIN" head disable claude >/dev/null 2>&1 || true
  systemctl --user unset-environment ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL >/dev/null 2>&1 || true
  sudo rm -rf /etc/codvps
}
trap cleanup EXIT

unit_active() { systemctl --user is-active --quiet "claude-remote@$1.service"; }
unit_enabled() { systemctl --user is-enabled --quiet "claude-remote@$1.service"; }
registered() { grep -Fxq -- "$1" "$REGISTRY" 2>/dev/null; }

# Mirrors the Claude CLI's own rule for ~/.claude.json workspace trust.
trusted() {
  python3 - "$HOME/.claude.json" "$1" <<'PY'
import json, sys
try:
    data = json.load(open(sys.argv[1]))
except (OSError, ValueError):
    sys.exit(1)
entry = (data.get("projects") or {}).get(sys.argv[2])
sys.exit(0 if isinstance(entry, dict) and entry.get("hasTrustDialogAccepted") is True else 1)
PY
}

# Drop one workspace's trust entry so a test proves its own command seeded it.
forget_trust() {
  python3 - "$HOME/.claude.json" "$1" <<'PY'
import json, os, sys
path, ws = sys.argv[1], sys.argv[2]
if not os.path.exists(path):
    sys.exit(0)
data = json.load(open(path))
if isinstance(data.get("projects"), dict):
    data["projects"].pop(ws, None)
with open(path, "w") as f:
    json.dump(data, f, indent=2)
    f.write("\n")
PY
}

consent_seeded() {
  python3 - "$HOME/.claude.json" <<'PY'
import json, sys
try:
    data = json.load(open(sys.argv[1]))
except (OSError, ValueError):
    sys.exit(1)
sys.exit(0 if isinstance(data, dict) and data.get("remoteDialogSeen") is True else 1)
PY
}

fixture() {
  local name=$1 src="$WORK/$1-source"
  git init -q -b main "$src"
  git -C "$src" config user.email "fakevps@example.invalid"
  git -C "$src" config user.name "Fakevps Test"
  printf '# %s\n' "$name" >"$src/README.md"
  git -C "$src" add -A
  git -C "$src" commit -q -m init
  git init -q --bare "$WORK/$name.git"
  git -C "$src" push -q "$WORK/$name.git" HEAD:main
}

# run <cmd...>: capture combined output in $OUT and exit status in $RC.
run() {
  set +e
  OUT=$("$@" 2>&1)
  RC=$?
  set -e
}

setup() {
  sudo loginctl enable-linger operator >/dev/null 2>&1 || true
  sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR

  "$CODVPS_BIN" head disable claude >/dev/null 2>&1 || true
  rm -rf "$WORK" "$CACHE" "$HOME/.claude" "$HOME/.claude.json" \
    "$HOME/hello" "$HOME/heritage" "$HOME/auto-attach" "$HOME/late-attach" "$HOME/trust-gap"
  rm -f "$REGISTRY" "$HOME/.config/codvps/codex-repositories" "$FLAG"
  mkdir -p "$WORK" "$CACHE"
  : >"$CACHE/invocations.log"
  for name in hello heritage auto-attach late-attach trust-gap; do
    fixture "$name"
  done

  # A head must never inherit provider credentials from the user manager.
  systemctl --user set-environment ANTHROPIC_API_KEY=fakevps-api-key \
    CLAUDE_CODE_OAUTH_TOKEN=fakevps-oauth-token ANTHROPIC_BASE_URL=https://example.invalid
}

test_login_claude_refused_unselected() {
  select_components '"codex"'
  run "$CODVPS_BIN" login claude
  [[ $RC -ne 0 ]] || note "login claude succeeded while claude was not a selected component"
  [[ $OUT == *'claude is not a selected component; select it first'* ]] ||
    note "login claude refusal is not actionable: $OUT"
  run "$CODVPS_BIN" head enable claude
  [[ $RC -ne 0 && $OUT == *'claude is not a selected component; rerun with it selected'* ]] ||
    note "head enable claude was not refused while claude was unselected (rc=$RC): $OUT"
  [[ ! -e $CRED ]] || note "a refused login still created a credential"
}

test_login_claude_uses_auth_login() {
  select_components '"claude","codex"'
  run "$CODVPS_BIN" login claude </dev/null
  [[ $RC -eq 0 ]] || note "login claude failed (rc=$RC): $OUT"
  [[ -f $CRED ]] || note "login claude did not produce $CRED"
  grep -Fxq 'auth login' "$CACHE/invocations.log" || note "login claude did not run 'claude auth login'"
  consent_seeded || note "login claude did not seed remoteDialogSeen in ~/.claude.json"
}

test_unit_installed() {
  [[ -f $UNIT_FILE ]] || note "$UNIT_FILE is not installed"
  grep -Fq 'UnsetEnvironment=ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL' "$UNIT_FILE" ||
    note "claude-remote@.service does not scrub provider credentials"
  grep -Fq 'ExecStart=/usr/local/bin/codvps internal shell-exec --provider claude -- claude remote-control' "$UNIT_FILE" ||
    note "claude-remote@.service does not start claude through codvps internal shell-exec"
  grep -Fxq 'Restart=always' "$UNIT_FILE" || note "claude-remote@.service is not Restart=always"
  grep -Fq -- '--no-create-session-in-dir' "$UNIT_FILE" || note "claude-remote@.service may open interactive sessions"
}

test_head_seeds_workspace_trust() {
  run "$CODVPS_BIN" repo add "$WORK/heritage.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add heritage failed (rc=$RC): $OUT"
  ! trusted "$HOME/heritage" || note "repo add seeded trust while the Claude head was off"
  ! unit_enabled heritage || note "repo add enabled a head that is switched off"

  run "$CODVPS_BIN" head disable claude
  [[ $RC -eq 0 && ! -e $FLAG ]] || note "head disable claude failed or left the flag (rc=$RC): $OUT"
  forget_trust "$HOME/heritage"

  run "$CODVPS_BIN" head enable claude
  [[ $RC -eq 0 ]] || note "head enable claude failed (rc=$RC): $OUT"
  [[ "$(stat -c '%U:%a' "$FLAG" 2>/dev/null)" == "$(id -un):600" ]] || note "head flag is not operator-owned mode 600"
  trusted "$HOME/heritage" || note "head enable claude did not seed workspace trust for heritage"
  unit_active heritage || note "head enable claude did not start claude-remote@heritage"
  grep -Fxq 'remote-control --spawn worktree --capacity 4 --no-create-session-in-dir --remote-control-session-name-prefix cloud-heritage' \
    "$CACHE/heritage.log" 2>/dev/null || note "heritage head did not run the expected remote-control command"
  [[ ! -e $CACHE/refusals.log ]] || note "Claude refused a workspace during head enable: $(cat "$CACHE/refusals.log")"

  run "$CODVPS_BIN" head disable claude
  [[ $RC -eq 0 && ! -e $FLAG ]] || note "head disable claude failed or left the flag (rc=$RC): $OUT"
  ! unit_active heritage || note "head disable left claude-remote@heritage active"
  ! unit_enabled heritage || note "head disable left claude-remote@heritage enabled"
  registered heritage || note "head disable deregistered heritage"
}

test_head_enable_attaches_every_registered_repo() {
  run "$CODVPS_BIN" repo add "$WORK/hello.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add hello failed (rc=$RC): $OUT"
  run "$CODVPS_BIN" head enable claude
  [[ $RC -eq 0 ]] || note "head enable claude failed (rc=$RC): $OUT"
  for name in heritage hello; do
    trusted "$HOME/$name" || note "head enable claude did not seed trust for $name"
    unit_active "$name" || note "head enable claude did not bring up $name"
  done
  [[ ! -e $CACHE/refusals.log ]] || note "Claude refused a workspace: $(cat "$CACHE/refusals.log")"
}

test_slice_budget() {
  [[ -f /etc/systemd/user/claude-remote.slice ]] || note "claude-remote.slice is not installed"
  [[ "$(systemctl --user show claude-remote@hello.service -p Slice --value)" == claude-remote.slice ]] ||
    note "claude-remote@hello is not in claude-remote.slice"
  [[ "$(systemctl --user show claude-remote.slice -p MemoryMax --value)" == 5368709120 ]] ||
    note "claude-remote.slice MemoryMax is not 5G"
}

test_remote_control_invocation() {
  local log="$CACHE/hello.log" attempt
  local -a lines=()
  for ((attempt = 1; attempt <= 50; attempt++)); do
    [[ -f $log ]] && mapfile -t lines <"$log"
    [[ ${#lines[@]} -eq 4 ]] && break
    sleep 0.1
  done
  if [[ ${#lines[@]} -ne 4 ]]; then
    note "expected one logged remote-control start for hello, got ${#lines[@]} lines"
    return
  fi
  [[ ${lines[0]} == 'remote-control --spawn worktree --capacity 4 --no-create-session-in-dir --remote-control-session-name-prefix cloud-hello' ]] ||
    note "unexpected remote-control arguments: ${lines[0]}"
  [[ ${lines[1]} == 'ENV_ANTHROPIC_API_KEY=' ]] || note "ANTHROPIC_API_KEY leaked into the head"
  [[ ${lines[2]} == 'ENV_CLAUDE_CODE_OAUTH_TOKEN=' ]] || note "CLAUDE_CODE_OAUTH_TOKEN leaked into the head"
  [[ ${lines[3]} == 'ENV_ANTHROPIC_BASE_URL=' ]] || note "ANTHROPIC_BASE_URL leaked into the head"
}

test_restart_always() {
  local pid restarts
  pid=$(systemctl --user show claude-remote@hello.service -p MainPID --value)
  if [[ ! $pid =~ ^[1-9][0-9]*$ ]]; then
    note "claude-remote@hello has no main PID"
    return
  fi
  kill -9 "$pid"
  sleep 8
  unit_active hello || note "claude-remote@hello did not come back after SIGKILL"
  restarts=$(systemctl --user show claude-remote@hello.service -p NRestarts --value)
  [[ $restarts =~ ^[0-9]+$ && $restarts -ge 1 ]] || note "NRestarts is $restarts after SIGKILL"
}

test_lifecycle_is_host_scoped() {
  run "$CODVPS_BIN" head disable claude
  [[ $RC -eq 0 ]] || note "head disable claude failed (rc=$RC): $OUT"
  ! unit_active hello || note "hello remained active after head disable claude"
  ! unit_active heritage || note "heritage remained active after head disable claude"
  registered hello || note "head disable deregistered hello"

  run "$CODVPS_BIN" head enable claude
  [[ $RC -eq 0 ]] || note "head enable claude failed (rc=$RC): $OUT"
  unit_active hello || note "hello did not come back on head enable claude"
  unit_active heritage || note "heritage did not come back on head enable claude"
}

expect_refusal() {
  local expected=$1
  shift
  run "$CODVPS_BIN" "$@"
  [[ $RC -ne 0 ]] || note "removed spelling unexpectedly succeeded: $*"
  [[ $OUT == *"$expected"* ]] || note "unexpected guidance for '$*': $OUT"
}

test_removed_command_spellings() {
  expect_refusal 'repo add no longer accepts --head or --trusted; heads are host-scoped, so use codvps enable <claude|codex> separately' \
    repo add --head none "$WORK/hello.git"
  expect_refusal 'repo add no longer accepts --head or --trusted' repo add --trusted "$WORK/hello.git"
  expect_refusal 'usage: codvps head enable claude' head enable claude hello
  expect_refusal 'usage: codvps head disable claude' head disable claude hello
  expect_refusal 'usage: codvps head enable codex' head enable codex hello
  registered hello || note "a removed spelling changed the registry"
  unit_active hello || note "a removed spelling stopped the hello head"
}

test_repo_add_auto_attaches() {
  run "$CODVPS_BIN" repo add "$WORK/auto-attach.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add auto-attach failed (rc=$RC): $OUT"
  trusted "$HOME/auto-attach" || note "repo add did not seed trust with the head on"
  unit_active auto-attach || note "repo add did not attach auto-attach to the enabled head"
  grep -Fxq 'remote-control --spawn worktree --capacity 4 --no-create-session-in-dir --remote-control-session-name-prefix cloud-auto-attach' \
    "$CACHE/auto-attach.log" 2>/dev/null || note "auto-attach head did not run the expected remote-control command"

  run "$CODVPS_BIN" head disable claude
  run "$CODVPS_BIN" repo add "$WORK/late-attach.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add late-attach failed (rc=$RC): $OUT"
  registered late-attach || note "repo add did not register late-attach"
  ! trusted "$HOME/late-attach" || note "repo add seeded trust with the head off"
  ! unit_enabled late-attach || note "repo add attached a head that is switched off"

  run "$CODVPS_BIN" head enable claude
  [[ $RC -eq 0 ]] || note "head enable claude failed (rc=$RC): $OUT"
  unit_active late-attach || note "head enable did not sweep up late-attach"
  unit_active auto-attach || note "head enable did not bring auto-attach back"
}

test_list_and_status() {
  local list status
  list=$("$CODVPS_BIN" head list)
  grep -Eq '^claude +host +- +enabled +-$' <<<"$list" || note "head list host row wrong: $list"
  grep -Eq '^claude +repo +hello +enabled +active$' <<<"$list" || note "head list hello row wrong: $list"
  status=$("$CODVPS_BIN" status)
  grep -Eq '^hello +active +running +[0-9]+$' <<<"$status" || note "status hello row wrong: $status"
  grep -Eq '^claude +enabled +- +-$' <<<"$status" || note "status claude host row wrong: $status"
}

test_old_commands() {
  expect_refusal 'removed: restart a head with systemctl' restart codex
  expect_refusal 'removed: use codvps update' update stage
  expect_refusal 'removed: use codvps update' update apply
}

test_fails_loudly_when_untrusted() {
  run "$CODVPS_BIN" head disable claude
  run "$CODVPS_BIN" repo add "$WORK/trust-gap.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add trust-gap failed (rc=$RC): $OUT"
  forget_trust "$HOME/trust-gap"
  printf '%s\n' "$HOME/trust-gap" >"$CACHE/refuse-workspaces"
  rm -f "$CACHE/refusals.log"

  run "$CODVPS_BIN" head enable claude
  [[ $RC -ne 0 ]] || note "head enable claude exited 0 while trust-gap crash-looped"
  [[ $OUT == *'Claude Remote Control for trust-gap failed to become active; the head was stopped to avoid a restart loop'* ]] ||
    note "unexpected crash-loop output: $OUT"
  ! unit_enabled trust-gap || note "a crash-looping head was left enabled"
  ! unit_active trust-gap || note "a crash-looping head was left running"
  grep -Fxq "$HOME/trust-gap" "$CACHE/refusals.log" 2>/dev/null || note "Claude did not record a refusal for trust-gap"

  rm -f "$CACHE/refuse-workspaces"
  run "$CODVPS_BIN" head enable claude
  [[ $RC -eq 0 ]] || note "head enable claude did not recover trust-gap (rc=$RC): $OUT"
  trusted "$HOME/trust-gap" || note "trust-gap is not trusted after recovery"
  unit_active trust-gap || note "trust-gap did not come up after recovery"
  rm -f "$CACHE/refusals.log"
}

test_never_interactive() {
  local non_remote
  [[ ! -e $CACHE/interactive-invocations.log ]] ||
    note "codvps opened an interactive Claude session for: $(cat "$CACHE/interactive-invocations.log")"
  non_remote=$(grep -v -e '^remote-control ' -e '^auth login$' -e '^ENV_' "$CACHE/invocations.log" || true)
  [[ -z $non_remote ]] || note "unexpected non-remote-control Claude invocations: $non_remote"
}

setup
test_login_claude_refused_unselected
test_login_claude_uses_auth_login
test_unit_installed
test_head_seeds_workspace_trust
test_head_enable_attaches_every_registered_repo
test_slice_budget
test_remote_control_invocation
test_restart_always
test_lifecycle_is_host_scoped
test_removed_command_spellings
test_repo_add_auto_attaches
test_list_and_status
test_old_commands
test_fails_loudly_when_untrusted
test_never_interactive

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more Claude head assertions failed"
  exit 1
fi
echo "PASS: login claude, head enable/disable/list claude, status, and repo auto-attach behave as the reference"
