#!/usr/bin/env bash
set -euo pipefail

# Test: vendor-managed CLIs, black-box against the product binary
# and units. codvps keeps no copies and pins no versions: `codvps update`
# runs each CLI's own updater and restarts only the active heads whose CLI
# changed, and install retires what the old pinned-runtime layer left.
#
# The vendor Claude is a wrapper at ~/.local/bin/claude (where the native
# installer puts it) that reports a version from a file and whose `update`
# bumps it; everything else goes to the image's fake claude. Nothing is
# downloaded.

BUILD=/opt/codvps-build/codvps
WORK="$HOME/tmp/vendor19"
REMOTE_HOME="$HOME/.codex"
COMPONENTS=/etc/codvps/components.json
HEAD=claude-remote@hello19.service
UNIT=codex-remote@operator.service
VENDOR_CLAUDE="$HOME/.local/bin/claude"
VERSION_FILE="$WORK/claude.version"
LEGACY_DROPIN=/etc/systemd/user/claude-remote@.service.d/runtime.conf
LEGACY_ROOT=/opt/codvps/runtimes
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

run() {
  set +e
  OUT=$("$@" 2>&1)
  RC=$?
  set -e
}

codvps() { "$BUILD" "$@"; }

claude_pid() { systemctl --user show "$HEAD" -p MainPID --value; }

cleanup() {
  codvps disable claude >/dev/null 2>&1 || true
  codvps repo remove hello19 >/dev/null 2>&1 || true
  rm -f "$VENDOR_CLAUDE"
  if [[ -f $WORK/components.orig ]]; then
    sudo install -m 0644 "$WORK/components.orig" "$COMPONENTS"
  fi
}
trap cleanup EXIT

setup() {
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
  sudo loginctl enable-linger operator >/dev/null 2>&1 || true
  sudo systemctl start "user@$(id -u).service" >/dev/null 2>&1 || true
  codvps disable codex >/dev/null 2>&1 || true
  codvps disable claude >/dev/null 2>&1 || true
  rm -rf "$WORK" "${HOME:?}/hello19"
  mkdir -p "$WORK" "$(dirname "$VENDOR_CLAUDE")"
  [[ -f $COMPONENTS ]] && cp -p "$COMPONENTS" "$WORK/components.orig"

  # The vendor Claude install.
  echo 1.0.0 >"$VERSION_FILE"
  cat >"$VENDOR_CLAUDE" <<SH
#!/bin/sh
case "\$1" in
  --version) echo "\$(cat '$VERSION_FILE') (Claude Code)" ;;
  update)
    if [ ! -f '$WORK/no-new-release' ]; then
      awk -F. '{ printf "%d.%d.%d\n", \$1, \$2, \$3 + 1 }' '$VERSION_FILE' >'$VERSION_FILE.new'
      mv '$VERSION_FILE.new' '$VERSION_FILE'
    fi ;;
  *) exec /usr/local/bin/claude "\$@" ;;
esac
SH
  chmod 0755 "$VENDOR_CLAUDE"

  # What an old pinned-runtime install left behind.
  sudo install -d "$(dirname "$LEGACY_DROPIN")" "$LEGACY_ROOT/claude/versions/0.0.0/bin"
  printf '[Service]\nExecStart=\nExecStart=%s/claude/versions/0.0.0/bin/claude remote-control\n' "$LEGACY_ROOT" |
    sudo tee "$LEGACY_DROPIN" >/dev/null

  # The Codex standalone install and login the heads expect.
  if [[ ! -x $REMOTE_HOME/packages/standalone/current/codex ]]; then
    install -d -m 0700 "$REMOTE_HOME" "$REMOTE_HOME/packages/standalone/releases/fake"
    install -m 0755 /usr/local/bin/codex "$REMOTE_HOME/packages/standalone/releases/fake/codex"
    ln -sfn releases/fake "$REMOTE_HOME/packages/standalone/current"
  fi
  [[ -s $REMOTE_HOME/auth.json ]] || { printf '{"fake":true}\n' >"$REMOTE_HOME/auth.json"; chmod 0600 "$REMOTE_HOME/auth.json"; }

  local src="$WORK/hello19-source"
  git init -q -b main "$src"
  git -C "$src" config user.email "fakevps@example.invalid"
  git -C "$src" config user.name "Fakevps Test"
  printf '# hello19\n' >"$src/README.md"
  git -C "$src" add -A
  git -C "$src" commit -q -m init
  git init -q --bare -b main "$WORK/hello19.git"
  git -C "$src" push -q "$WORK/hello19.git" HEAD:main
}

test_install_retires_the_pinned_runtimes() {
  run sudo "$BUILD" install --components claude,codex --switch none --skip-provision </dev/null
  [[ $RC -eq 0 ]] || { note "install failed (rc=$RC): $OUT"; return; }
  [[ $OUT == *'removed the pinned runtime copies'* ]] || note "install did not report retiring the pinned runtimes: $OUT"
  [[ ! -e $LEGACY_DROPIN && ! -e $LEGACY_ROOT ]] || note "install left the pinning drop-in or $LEGACY_ROOT"
  for unit in /etc/systemd/system/codvps-updater@.service /etc/systemd/system/codvps-runtime-publish@.path; do
    [[ ! -e $unit ]] || note "install still ships $unit"
  done
  run codvps doctor
  [[ $OUT != *'pinned runtime'* && $OUT != *'session hook'* ]] || note "doctor still reports pinned-runtime state: $OUT"
}

test_heads_run_the_vendor_cli() {
  codvps login claude </dev/null >/dev/null 2>&1 || true
  run codvps repo add "$WORK/hello19.git"
  [[ $RC -eq 0 ]] || note "repo add failed (rc=$RC): $OUT"
  run codvps enable claude
  [[ $RC -eq 0 ]] || note "enable claude failed (rc=$RC): $OUT"
  systemctl --user is-active --quiet "$HEAD" || note "$HEAD is not active"
  [[ "$(systemctl --user show "$HEAD" -p ExecStart)" != *"$LEGACY_ROOT"* ]] || note "the Claude head still runs a pinned copy"
}

test_update_without_a_terminal_never_restarts() {
  local pid
  pid=$(claude_pid)
  run bash -c "'$BUILD' update claude </dev/null"
  [[ $RC -eq 0 ]] || note "update claude failed (rc=$RC): $OUT"
  [[ $OUT == *'claude: 1.0.0 (Claude Code) -> 1.0.1 (Claude Code)'* ]] || note "update did not report the vendor update: $OUT"
  [[ $OUT == *"Rerun with --yes to restart"*"$HEAD"* ]] || note "update did not name the restart and --yes: $OUT"
  [[ $OUT != *'Type yes'* ]] || note "update prompted without a terminal: $OUT"
  [[ "$(claude_pid)" == "$pid" ]] || note "update restarted the head without --yes"
}

test_update_with_yes_restarts_only_changed_active_heads() {
  local pid
  pid=$(claude_pid)
  run codvps update claude --yes
  [[ $RC -eq 0 && $OUT == *'claude: 1.0.1 (Claude Code) -> 1.0.2 (Claude Code)'* && $OUT == *"restarted $HEAD"* ]] ||
    { note "update --yes did not update and restart (rc=$RC): $OUT"; return; }
  [[ "$(claude_pid)" != "$pid" ]] || note "the Claude head was not restarted"
  systemctl --user is-active --quiet "$HEAD" || note "$HEAD did not come back"
  ! systemctl is-active --quiet "$UNIT" || note "update started the disabled Codex head"

  touch "$WORK/no-new-release"
  pid=$(claude_pid)
  run codvps update claude --yes
  [[ $RC -eq 0 && $OUT == *'claude: 1.0.2 (Claude Code) is current'* && $OUT != *restarted* ]] ||
    note "an update with nothing new restarted something (rc=$RC): $OUT"
  [[ "$(claude_pid)" == "$pid" ]] || note "an unchanged CLI's head was restarted"
}

test_update_follows_the_component_selection() {
  run codvps update cursor
  [[ $RC -ne 0 && $OUT == *'cursor is not a selected component'* ]] || note "update cursor on a claude,codex host was not refused (rc=$RC): $OUT"
  run codvps update bogus
  [[ $RC -ne 0 && $OUT == *'unknown provider: bogus'* ]] || note "an unknown provider was not refused (rc=$RC): $OUT"
}

test_pinned_runtime_commands_are_gone() {
  run codvps runtime path codex
  [[ $RC -ne 0 && $OUT == *'unknown command'* ]] || note "runtime still exists (rc=$RC): $OUT"
  run codvps internal session-check </dev/null
  [[ $RC -eq 0 && -z $OUT ]] || note "a leftover session-check hook would fail sessions (rc=$RC): $OUT"
  run codvps help
  [[ $OUT != *'runtime'* && $OUT == *'update [provider] [--yes]'* ]] || note "help still advertises pinned runtimes: $OUT"
}

setup
test_install_retires_the_pinned_runtimes
test_heads_run_the_vendor_cli
test_update_without_a_terminal_never_restarts
test_update_with_yes_restarts_only_changed_active_heads
test_update_follows_the_component_selection
test_pinned_runtime_commands_are_gone

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: vendor-managed CLI updates"
