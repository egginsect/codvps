#!/usr/bin/env bash
set -euo pipefail

# Test: codvps login codex, head enable|disable|list|pair|ensure codex,
# status, and repo add/remove against the Codex head, running the product
# codex-remote@.service template and watchdog units (installed by the
# Dockerfile from internal/systemd/units) with the fake codex, whose
# Remote Control daemon listens on the real control-socket path.
#
# Covers (see docs/test-parity.md for the full mapping):
#   - test_components_login_codex_refused_unselected          (tests/components.sh)
#   - test_install_fake_codex_and_cred                         (tests/smoke.sh)
#   - test_login_codex_uses_device_auth                        (tests/smoke.sh)
#   - test_codex_unit_installed                                (tests/smoke.sh)
#   - test_codex_remote_state_is_separate                      (tests/smoke.sh)
#   - test_codex_head_starts_without_claude_credential         (tests/smoke.sh)
#   - test_codex_head_list_status_and_pair                     (tests/smoke.sh)
#   - test_codex_disable_enable_and_credential_gate            (tests/smoke.sh)
#   - test_codex_start_recovers_one_transient_errored_connection (tests/smoke.sh)
#   - test_codex_managed_start_recovers_systemd_rate_limit     (tests/smoke.sh)
#   - test_head_command_surface                                (tests/smoke.sh)
#   plus the watchdog (head ensure codex, test-codex-watchdog in
#   docs/design/go-core.md).
#
# Removed by owner decision (codvps no longer sandboxes Codex Remote:
# no mount namespace, membership manifest or revocation): the reference's
# curated-workspace, membership-manifest and revocation scenarios have no
# counterpart. In their place, test_shared_daemon checks that the daemon is
# the ordinary one for ~/.codex and that repo add/remove never touch it.
#
# Adaptations: test_head_command_surface asserts codvps's own help lines
# for the head and repo commands (the top-level help is codvps's, not the
# reference's word for word). The reference's `doctor` assertions inside these scenarios
# belong to and are not made here. The reference forces the
# Every `head enable codex` here stops the watchdog timer it starts
# (except in the watchdog test), so a timer tick cannot race assertions
# about a deliberately dead daemon.

WORK="$HOME/tmp/head16"
REMOTE_HOME="$HOME/.codex"
CACHE="$REMOTE_HOME/cache/fake-codex"
CRED="$HOME/.claude/.credentials.json"
STATE="$HOME/.config/codvps"
REGISTRY="$STATE/repositories"
COMPONENTS=/etc/codvps/components.json
UNIT=codex-remote@operator.service
UNIT_FILE=/etc/systemd/system/codex-remote@.service
SOCKET="$REMOTE_HOME/app-server-control/app-server-control.sock"
TIMER=codex-remote-watchdog.timer
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

select_components() {
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":[%s],"switch":"none"}\n' "$1" | sudo tee "$COMPONENTS" >/dev/null
  sudo chmod 0644 "$COMPONENTS"
}

cleanup() {
  sudo chown "$(id -un):$(id -gn)" "$REMOTE_HOME" >/dev/null 2>&1 || true
  "$CODVPS_BIN" head disable codex >/dev/null 2>&1 || true
  sudo rm -rf /etc/codvps
}
trap cleanup EXIT

# run <cmd...>: capture combined output in $OUT and exit status in $RC.
run() {
  set +e
  OUT=$("$@" 2>&1)
  RC=$?
  set -e
}

unit_active() { systemctl is-active --quiet "$UNIT"; }
daemon_reachable() { CODEX_HOME="$REMOTE_HOME" codex app-server daemon version >/dev/null 2>&1; }
daemon_pid() { cat "$CACHE/remote-control.pid" 2>/dev/null || true; }
spawns() { wc -l <"$CACHE/daemon-spawns.log"; }
starts() { grep -Fxc 'remote-control start --json' "$CACHE/invocations.log" || true; }
registered() { grep -Fxq -- "$1" "$REGISTRY" 2>/dev/null; }

# enable_codex: head enable codex, then stop the watchdog timer it started.
enable_codex() {
  run "$CODVPS_BIN" head enable codex
  systemctl --user stop "$TIMER" >/dev/null 2>&1 || true
}

wait_dead() {
  local pid=$1 attempt
  for ((attempt = 1; attempt <= 30; attempt++)); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  return 1
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

setup() {
  sudo loginctl enable-linger operator >/dev/null 2>&1 || true
  sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR

  "$CODVPS_BIN" head disable codex >/dev/null 2>&1 || true
  "$CODVPS_BIN" head disable claude >/dev/null 2>&1 || true
  sudo chown "$(id -un):$(id -gn)" "$REMOTE_HOME" >/dev/null 2>&1 || true
  rm -rf "$WORK" "$REMOTE_HOME" "$HOME/.codex" "$HOME/.claude"
  for name in codex-one codex-two codex-unlisted; do
    rm -rf "${HOME:?}/$name"
  done
  rm -f "$REGISTRY"
  mkdir -p "$WORK"
  for name in codex-one codex-two codex-unlisted; do
    fixture "$name"
  done
}

# test_install_fake_codex_and_cred: the standalone layout the unit runs,
# pointing at the fake, plus a Claude credential (the Codex head must not
# need it). The Codex login itself comes from `codvps login codex` below.
install_fake_codex() {
  install -d -m 0700 "$REMOTE_HOME" "$REMOTE_HOME/packages/standalone/releases/fake" "$CACHE"
  install -m 0755 /usr/local/bin/codex "$REMOTE_HOME/packages/standalone/releases/fake/codex"
  ln -sfn releases/fake "$REMOTE_HOME/packages/standalone/current"
  : >"$CACHE/invocations.log"
  : >"$CACHE/daemon-spawns.log"
  install -d -m 0700 "$HOME/.claude"
  printf '{"fake":"claude"}\n' >"$CRED"
  chmod 0600 "$CRED"
}

test_login_codex_refused_unselected() {
  select_components '"claude"'
  run "$CODVPS_BIN" login codex </dev/null
  [[ $RC -ne 0 && $OUT == *'codex is not a selected component; select it first'* ]] ||
    note "login codex was not refused on a claude-only host (rc=$RC): $OUT"
  run "$CODVPS_BIN" head enable codex
  [[ $RC -ne 0 && $OUT == *'codex is not a selected component; rerun with it selected'* ]] ||
    note "head enable codex was not refused on a claude-only host (rc=$RC): $OUT"
  [[ ! -e $REMOTE_HOME/auth.json ]] || note "a refused login still created auth.json"
}

test_login_codex_uses_device_auth() {
  select_components '"claude","codex"'
  run "$CODVPS_BIN" login codex </dev/null
  [[ $RC -eq 0 ]] || note "login codex failed (rc=$RC): $OUT"
  [[ -s $REMOTE_HOME/auth.json ]] || note "login codex did not create $REMOTE_HOME/auth.json"
  grep -Fxq 'login --device-auth' "$CACHE/invocations.log" || note "login codex did not run 'codex login --device-auth'"
}

test_codex_unit_installed() {
  local line
  [[ -f $UNIT_FILE ]] || note "$UNIT_FILE is not installed"
  for line in 'Type=oneshot' 'RemainAfterExit=yes' 'User=%i' 'WorkingDirectory=/home/%i' \
    'ExecStart=/usr/local/bin/codvps internal shell-exec --provider codex -- /usr/local/bin/codvps internal codex-remote-start' \
    'ExecStop=/home/%i/.codex/packages/standalone/current/codex remote-control stop --json'; do
    grep -Fxq -- "$line" "$UNIT_FILE" || note "codex-remote@.service is missing: $line"
  done
  #: no codvps sandbox around the ordinary Codex daemon.
  for line in ExecStartPre= ProtectHome= ProtectSystem= PrivateTmp= NoNewPrivileges= RestrictSUIDSGID= \
    CapabilityBoundingSet= BindPaths= BindReadOnlyPaths= TemporaryFileSystem= Environment=CODEX_HOME=; do
    ! grep -q "^$line" "$UNIT_FILE" || note "codex-remote@.service still declares $line"
  done
  [[ ! -e /etc/systemd/user/codex-remote.service ]] || note "a legacy --user codex-remote.service is installed"
  [[ -f /etc/systemd/user/$TIMER ]] || note "the watchdog timer is not installed"
}

test_codex_remote_state_is_separate() {
  [[ -d $REMOTE_HOME && ! -L $REMOTE_HOME ]] || note "$REMOTE_HOME is not a real directory"
  [[ "$(stat -c '%U:%a' "$REMOTE_HOME")" == "$(id -un):700" ]] || note "$REMOTE_HOME is not operator-owned mode 700"
  [[ -x $REMOTE_HOME/packages/standalone/current/codex ]] || note "the standalone codex is not executable"
  [[ ! -e $REMOTE_HOME/state_5.sqlite && ! -e $REMOTE_HOME/sessions ]] ||
    note "interactive Codex state leaked into the Remote home"
}

# The daemon is the ordinary Codex app-server daemon for ~/.codex: it runs as
# the operator with the operator's own view of the host, answers on the real
# control socket the host codex CLI connects to, and registering or removing
# repositories neither restarts nor stops it.
test_shared_daemon() {
  local first before list
  git clone -q "$WORK/codex-unlisted.git" "$HOME/codex-unlisted"
  run "$CODVPS_BIN" repo add "$WORK/codex-one.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add codex-one failed (rc=$RC): $OUT"
  ! unit_active || note "repo add started the Codex unit"
  [[ ! -e $STATE/codex-repositories ]] || note "repo add wrote a Codex membership manifest"
  before=$(spawns)

  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex failed (rc=$RC): $OUT"
  unit_active || note "head enable codex did not start $UNIT"
  first=$(daemon_pid)
  if [[ ! $first =~ ^[1-9][0-9]*$ ]] || ! kill -0 "$first" 2>/dev/null; then
    note "no live Codex daemon after head enable codex"
    return
  fi
  [[ ! -L $SOCKET && -S $SOCKET ]] || note "the control socket at $SOCKET is not a plain socket"
  daemon_reachable || note "the daemon is unreachable from the host codex CLI"
  [[ $(spawns) -eq $((before + 1)) ]] || note "head enable codex spawned $(($(spawns) - before)) daemons, want 1"
  # The unit adds no sandbox: the daemon sees the whole host as the operator.
  [[ -e /proc/$first/root$HOME/codex-unlisted && "$(readlink "/proc/$first/root")" == / ]] ||
    note "the daemon does not share the host filesystem view"

  run "$CODVPS_BIN" repo add "$WORK/codex-two.git" </dev/null
  [[ $RC -eq 0 ]] || note "repo add codex-two with Codex on failed (rc=$RC): $OUT"
  [[ "$(daemon_pid)" == "$first" && $(spawns) -eq $((before + 1)) ]] || note "repo add restarted the Codex daemon"
  list=$("$CODVPS_BIN" repo list)
  grep -Eq "^codex-two +$HOME/codex-two +-$" <<<"$list" || note "repo list shows a Codex column or misses codex-two: $list"
  [[ $list == *'codex=enabled/running'* ]] || note "repo list does not report the Codex head: $list"

  run "$CODVPS_BIN" repo remove codex-two
  [[ $RC -eq 0 ]] || note "repo remove codex-two failed (rc=$RC): $OUT"
  [[ $OUT != *'Codex remains stopped'* ]] || note "repo remove still revokes Codex: $OUT"
  unit_active || note "repo remove stopped the Codex head"
  daemon_reachable || note "repo remove stopped the Codex daemon"
  [[ "$(daemon_pid)" == "$first" ]] || note "repo remove restarted the Codex daemon"
  ! registered codex-two || note "codex-two is still registered"
  [[ -d $HOME/codex-two/.git ]] || note "repo remove deleted the codex-two checkout"
}

# The retired sandboxed unit left the control socket a symlink into its
# private /tmp. Enabling the head removes exactly such a dangling link.
test_dangling_control_socket_link() {
  run "$CODVPS_BIN" head disable codex
  [[ $RC -eq 0 ]] || note "head disable codex failed (rc=$RC): $OUT"
  rm -f -- "$SOCKET"
  ln -s /tmp/systemd-private-gone/tmp/app-server-control.sock "$SOCKET"
  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex with a dangling control socket link failed (rc=$RC): $OUT"
  [[ ! -L $SOCKET && -S $SOCKET ]] || note "the dangling control socket link survived head enable codex"
  daemon_reachable || note "the daemon is unreachable after replacing the dangling link"
}

test_starts_without_claude_credential() {
  local pid
  run "$CODVPS_BIN" head disable codex
  mv "$CRED" "$CRED.bak"
  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex without a Claude credential failed (rc=$RC): $OUT"
  pid=$(daemon_pid)
  if [[ ! $pid =~ ^[1-9][0-9]*$ ]] || ! kill -0 "$pid" 2>/dev/null; then
    note "no live daemon without a Claude credential"
  fi
  mv "$CRED.bak" "$CRED"
  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex after restoring the credential failed (rc=$RC): $OUT"
}

test_list_status_and_pair() {
  local list status dead recovered
  list=$("$CODVPS_BIN" head list)
  grep -Eq '^codex +host +- +enabled +running$' <<<"$list" || note "head list codex row wrong: $list"
  grep -Eq '^claude +host ' <<<"$list" || note "head list lost the claude row: $list"
  status=$("$CODVPS_BIN" status)
  grep -Eq '^codex +active +exited +running$' <<<"$status" || note "status codex row wrong: $status"
  run "$CODVPS_BIN" head pair codex
  [[ $RC -eq 0 && $OUT == *'"status":"paired"'* ]] || note "head pair codex failed (rc=$RC): $OUT"
  [[ $OUT != *'session hook'* ]] || note "head pair codex still mentions the retired session hook: $OUT"
  grep -Fxq 'remote-control pair --json' "$CACHE/invocations.log" || note "head pair did not delegate to codex"

  # A oneshot stays active after its daemon dies; liveness is the socket.
  dead=$(daemon_pid)
  kill "$dead"
  wait_dead "$dead" || note "the fake daemon did not exit"
  unit_active || note "the oneshot unit went inactive with its daemon"
  list=$("$CODVPS_BIN" head list)
  grep -Eq '^codex +host +- +enabled +unavailable$' <<<"$list" || note "head list reports a dead daemon as running: $list"
  status=$("$CODVPS_BIN" status)
  grep -Eq '^codex +active +exited +unavailable$' <<<"$status" || note "status reports a dead daemon as running: $status"
  [[ $status == *'(daemon unreachable from this session: cannot connect to the app-server control socket'* ]] ||
    note "status does not explain the unreachable daemon: $status"

  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex did not repair a dead daemon (rc=$RC): $OUT"
  recovered=$(daemon_pid)
  [[ $recovered =~ ^[1-9][0-9]*$ && $recovered != "$dead" ]] || note "no new daemon after re-enable"
  [[ "$(cat "$CACHE/daemon-cwd")" == "$HOME" ]] || note "the recovered daemon runs elsewhere"
  daemon_reachable || note "the recovered daemon is unreachable"
}

test_disable_enable_and_credential_gate() {
  local old new
  old=$(daemon_pid)
  run "$CODVPS_BIN" head disable codex
  [[ $RC -eq 0 ]] || note "head disable codex failed (rc=$RC): $OUT"
  ! unit_active || note "the Codex unit stayed active after head disable"
  ! kill -0 "$old" 2>/dev/null || note "the Codex daemon survived head disable"
  ! systemctl --user is-enabled --quiet "$TIMER" || note "head disable left the watchdog enabled"

  CODEX_HOME="$REMOTE_HOME" codex remote-control start --json >/dev/null
  run "$CODVPS_BIN" head pair codex
  [[ $RC -ne 0 && $OUT == *'head is disabled'* ]] || note "head pair accepted an unmanaged daemon (rc=$RC): $OUT"
  CODEX_HOME="$REMOTE_HOME" codex remote-control stop --json >/dev/null

  mv "$REMOTE_HOME/auth.json" "$REMOTE_HOME/auth.json.bak"
  run "$CODVPS_BIN" head enable codex
  mv "$REMOTE_HOME/auth.json.bak" "$REMOTE_HOME/auth.json"
  [[ $RC -ne 0 && $OUT == *'login codex'* ]] || note "head enable codex without auth.json was not refused (rc=$RC): $OUT"

  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex failed (rc=$RC): $OUT"
  unit_active || note "the Codex unit is not active after re-enable"
  new=$(daemon_pid)
  [[ $new =~ ^[1-9][0-9]*$ && $new != "$old" ]] || note "re-enable did not start a new daemon"
}

test_watchdog() {
  local dead
  run "$CODVPS_BIN" head enable codex
  [[ $RC -eq 0 ]] || note "head enable codex failed (rc=$RC): $OUT"
  systemctl --user is-enabled --quiet "$TIMER" || note "head enable codex did not enable the watchdog timer"
  systemctl --user stop "$TIMER"
  run "$CODVPS_BIN" head ensure codex
  [[ $RC -eq 0 ]] || note "head ensure codex with a live daemon failed (rc=$RC): $OUT"
  dead=$(daemon_pid)
  kill "$dead"
  wait_dead "$dead" || note "the fake daemon did not exit"
  run "$CODVPS_BIN" head ensure codex
  [[ $RC -eq 0 ]] || note "head ensure codex did not recover a dead daemon (rc=$RC): $OUT"
  if [[ "$(daemon_pid)" == "$dead" ]] || ! daemon_reachable; then
    note "the watchdog did not bring the daemon back"
  fi
  [[ ! -e $HOME/.local/state/codvps/codex-watchdog.failures ]] || note "a successful recovery left watchdog failures behind"
}

test_start_recovers_one_transient_error() {
  local before after journal
  enable_codex
  before=$(starts)
  touch "$CACHE/fail-next-start-errored"
  sudo systemctl restart "$UNIT" || note "a transient errored relay failed the restart"
  unit_active || note "the unit is not active after a transient error"
  daemon_reachable || note "the daemon is unreachable after a transient error"
  after=$(starts)
  [[ $after -eq $((before + 2)) ]] || note "expected exactly one start retry, saw $((after - before)) starts"
  journal=$(sudo journalctl -u "$UNIT" -n 30 --no-pager -o cat)
  [[ $journal == *'Remote control is enabled on '*' but the connection is errored.'* ]] || note "the journal lacks Codex's errored-relay line"
  [[ $journal == *'retrying once'* ]] || note "the journal does not say the start was retried"

  before=$after
  touch "$CACHE/fail-start-errored-always"
  if sudo systemctl restart "$UNIT"; then
    note "a persistent errored relay was masked by the retry"
  fi
  after=$(starts)
  [[ $after -eq $((before + 2)) ]] || note "a persistent error caused $((after - before)) starts, want 2"
  ! unit_active || note "the unit is active despite a persistent error"
  rm -f "$CACHE/fail-start-errored-always"
  sudo systemctl start "$UNIT" || note "the unit did not start once the error cleared"
}

test_managed_start_recovers_rate_limit() {
  local attempt limit_hit=0 result
  enable_codex
  for ((attempt = 1; attempt <= 10; attempt++)); do
    if ! sudo systemctl restart "$UNIT" >/dev/null 2>&1; then
      limit_hit=1
      break
    fi
  done
  [[ $limit_hit -eq 1 ]] || { note "could not reproduce the Codex start-rate limit"; return; }
  result=$(systemctl show "$UNIT" -p Result --value)
  [[ $result == start-limit-hit ]] || note "the unit failed with $result, not start-limit-hit"
  enable_codex
  [[ $RC -eq 0 ]] || note "head enable codex did not recover start-limit-hit (rc=$RC): $OUT"
  unit_active || note "the unit is not active after the managed start"
  daemon_reachable || note "the daemon is unreachable after the managed start"
}

help_line() { printf '  %-28s %s' "$1" "$2"; }

test_head_command_surface() {
  local help bare repo_help repo_flag_help line
  help=$("$CODVPS_BIN" help)
  bare=$("$CODVPS_BIN")
  [[ $bare == "$help" ]] || note "bare codvps does not print the help"
  for line in \
    "$(help_line 'login codex' 'Authenticate Codex')" \
    "$(help_line 'repo add <url>' 'Clone or reattach a repository and attach it to every enabled head')" \
    "$(help_line 'repo list' 'List registered repositories and each head'"'"'s state')" \
    "$(help_line 'repo remove <name>' 'Deregister a repository and detach it from every head; keep the checkout')" \
    "$(help_line 'list' 'Show host heads and per-repository heads')" \
    "$(help_line 'pair codex' 'Print a short-lived Codex pairing code as JSON')"; do
    grep -Fxq -- "$line" <<<"$help" || note "codvps help is missing: $line"
  done
  # head enable/disable commands are long and wrap; check for presence instead of exact match
  for check in '  enable <claude|' '  disable <claude|' 'Enable a host head' 'Disable a host head'; do
    grep -Fq -- "$check" <<<"$help" || note "codvps help is missing substring: $check"
  done
  for line in 'repo claude' 'repo codex' 'repo enable' 'repo disable' 'repo pick' 'codex up' 'codex down' \
    'codex pair' 'codex logs' 'head logs' 'head ensure' '--head' '--trusted' 'login agy' \
    'runtime exec codex' 'runtime path codex' 'components list' 'components status'; do
    [[ $help != *"$line"* ]] || note "codvps help advertises a removed or hidden spelling: $line"
  done
  repo_help=$("$CODVPS_BIN" repo help)
  repo_flag_help=$("$CODVPS_BIN" repo --help)
  [[ $repo_help == "$repo_flag_help" ]] || note "repo help and repo --help differ"
  for line in 'Usage: codvps repo <command>' 'Repository commands:' \
    '  add <url>          Clone or safely reattach a repository and attach it to every enabled head' \
    '  list               List registered repositories and their Claude head state' \
    '  remove <name>      Deregister a repository and detach it from every head; keep the checkout' \
    'Heads are host-scoped, not per repository: codvps enable|disable <claude|codex|cursor>.' \
    'Every registered repository is attached to every enabled head.'; do
    grep -Fxq -- "$line" <<<"$repo_help" || note "codvps repo help is missing: $line"
  done
  for line in 'claude on' 'codex on' 'repo enable' 'repo disable' 'pick' '--trusted'; do
    [[ $repo_help != *"$line"* ]] || note "codvps repo help advertises a removed spelling: $line"
  done
  run "$CODVPS_BIN" nonsense
  [[ $RC -ne 0 && $OUT == *'unknown command: nonsense'* ]] || note "unexpected unknown-command output (rc=$RC): $OUT"
  run "$CODVPS_BIN" repo nonsense
  [[ $RC -ne 0 && $OUT == *'unknown repo command: nonsense'* ]] || note "unexpected unknown-repo-command output (rc=$RC): $OUT"
  run "$CODVPS_BIN" head nonsense
  [[ $RC -ne 0 && $OUT == *'unknown head command: nonsense'* ]] || note "unexpected unknown-head-command output (rc=$RC): $OUT"
}

setup
test_head_command_surface
install_fake_codex
test_login_codex_refused_unselected
test_login_codex_uses_device_auth
test_codex_unit_installed
test_codex_remote_state_is_separate
test_shared_daemon
test_dangling_control_socket_link
test_starts_without_claude_credential
test_list_status_and_pair
test_disable_enable_and_credential_gate
test_watchdog
test_start_recovers_one_transient_error
test_managed_start_recovers_rate_limit

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more Codex head assertions failed"
  exit 1
fi
echo "PASS: login codex, head enable/disable/list/pair/ensure codex and status behave as the reference, and the Codex daemon is the ordinary shared one"
