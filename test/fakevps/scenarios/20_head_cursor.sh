#!/usr/bin/env bash
set -euo pipefail

# Test: codvps login cursor, enable|disable cursor, and cursor head
# reconciliation against the product cursor-remote@.service template
# (installed by the Dockerfile from internal/systemd/units) and the fake
# agent CLI.
#
# Covers (see docs/test-parity.md for the full Cursor head mapping):
#   - login cursor is gated on cursor being selected
#   - enable cursor with login succeeds
#   - disable cursor removes the flag
#   - Cursor head unit template has no ~ paths and starts the worker through
#     codvps internal remote-start

[[ -n "${HOME:-}" ]] || { echo "FAIL: HOME not set"; exit 1; }
[[ -n "${CODVPS_BIN:-}" ]] || { echo "FAIL: CODVPS_BIN not set"; exit 1; }

WORK="$HOME/tmp/head20"
EXTERNAL_ROOT=""
WORKER_LOG="$HOME/.cache/fake-cursor/workers.log"
FLAG="$HOME/.config/codvps/cursor-head-enabled"
COMPONENTS=/etc/codvps/components.json
UNIT_FILE=/etc/systemd/user/cursor-remote@.service
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

select_components() {
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":[%s],"switch":"none"}\n' "$1" | sudo tee "$COMPONENTS" >/dev/null
  sudo chmod 0644 "$COMPONENTS"
}

# Invoked indirectly by the EXIT trap.
# shellcheck disable=SC2317,SC2329
cleanup() {
  "$CODVPS_BIN" disable cursor >/dev/null 2>&1 || true
  "$CODVPS_BIN" repo remove cursor20 >/dev/null 2>&1 || true
  if [[ -n $EXTERNAL_ROOT ]]; then
    "$CODVPS_BIN" config unset repo_root >/dev/null 2>&1 || true
    rm -rf "$EXTERNAL_ROOT"
  fi
  sudo rm -f /usr/local/bin/agent /tmp/agent
  rm -rf "$HOME/.cursor"
  sudo rm -rf /etc/codvps
}
trap cleanup EXIT

# docker exec is not a login shell: start the operator's user manager and
# name its bus, as the other head scenarios do.
sudo loginctl enable-linger operator >/dev/null 2>&1 || true
sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
XDG_RUNTIME_DIR="/run/user/$(id -u)"
export XDG_RUNTIME_DIR

# Select cursor as a component in components.json
select_components '"claude","codex","cursor"'

# Create agent in both ~/.local/bin (for After) and /usr/local/bin (for PATH)
mkdir -p "$HOME/.local/bin"

cat > "$HOME/.local/bin/agent" << 'AGENT'
#!/usr/bin/env bash
case "$1" in
  login)
    mkdir -p "$HOME/.cursor"
    touch "$HOME/.cursor/auth.json"
    exit 0
    ;;
  status)
    if [[ "${2:-}" == "--format" && "${3:-}" == "json" ]]; then
      # Return JSON status (exit code always 0)
      if [[ -f "$HOME/.cursor/auth.json" ]]; then
        echo '{"isAuthenticated":true,"status":"authenticated"}'
      else
        echo '{"isAuthenticated":false,"status":"unauthenticated"}'
      fi
      exit 0
    else
      # Legacy: bare "agent status" for testing
      [[ -f "$HOME/.cursor/auth.json" ]] && exit 0 || exit 1
    fi
    ;;
  worker)
    worker_dir=""
    shift
    # Parse options before "start" subcommand
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --worker-dir)
          worker_dir=$2
          shift 2
          ;;
        --name|--data-dir|--management-addr)
          shift 2
          ;;
        start)
          # Reached the start subcommand; options are in correct order
          shift
          [[ -d "$worker_dir" ]] || exit 127
          mkdir -p "$HOME/.cache/fake-cursor"
          printf '%s\t%s\n' "$PWD" "$worker_dir" >>"$HOME/.cache/fake-cursor/workers.log"
          # Worker stays connected until stopped
          exec sleep infinity
          ;;
        *)
          # Any option after "start" is an error
          echo "Error: invalid option after start: $1" >&2
          exit 127
          ;;
      esac
    done
    echo "Error: missing 'start' subcommand" >&2
    exit 127
    ;;
  --version)
    echo "2026.09.28-64d2043"
    exit 0
    ;;
  *)
    exit 127
    ;;
esac
AGENT
chmod +x "$HOME/.local/bin/agent"

# Also install in /usr/local/bin so login can find it in PATH
sudo install -m 0755 "$HOME/.local/bin/agent" /usr/local/bin/agent

# The harness installs codvps with cursor selected; verify the unit file exists
if [[ ! -f "$UNIT_FILE" ]]; then
  note "cursor-remote@.service not installed at $UNIT_FILE"
  exit $FAILED
fi

echo "OK: unit file found at $UNIT_FILE"

# The unit names no home-relative (~/) path.
# shellcheck disable=SC2088
if grep -q '~/' "$UNIT_FILE"; then
  note "cursor-remote@.service has relative paths (contains ~)"
fi

# The worker argv (worker --name <hostname>-<repo> --worker-dir . --data-dir
# ~/.local/share/cursor-agent/codvps-workers/<repo> start) comes from codvps
# internal remote-start, the start manual mode shares; the unit only names the
# provider and repository. The received argv is checked below.
if ! grep -Fxq 'ExecStart=/usr/local/bin/codvps internal remote-start --provider cursor --repo %i' "$UNIT_FILE"; then
  note "cursor-remote@.service ExecStart must start the worker through codvps internal remote-start"
fi

if ! grep -Fxq 'WorkingDirectory=%h' "$UNIT_FILE"; then
  note "Cursor must resolve the registered checkout after starting from HOME"
fi

if ! grep -Fxq 'Environment="NO_OPEN_BROWSER=1"' "$UNIT_FILE"; then
  note "cursor-remote@.service must keep Environment=\"NO_OPEN_BROWSER=1\""
fi

# Test: login cursor
echo "DEBUG: Running login cursor..."
if "$CODVPS_BIN" login cursor >/tmp/cursor_login.log 2>&1; then
  echo "DEBUG: login cursor succeeded"
else
  echo "DEBUG: login cursor failed with rc=$?"
  cat /tmp/cursor_login.log >&2
fi

if [[ ! -f "$HOME/.cursor/auth.json" ]]; then
  note "cursor login did not create credential"
  echo "DEBUG: Expected file at $HOME/.cursor/auth.json"
fi

# Register a real Git checkout outside HOME, with a space in its parent path.
# Unset the root before enabling Cursor, so only the persisted registry path
# can supply the worker's working directory.
rm -rf "$WORK"
rm -f "$WORKER_LOG"
mkdir -p "$WORK"
git init -q -b main "$WORK/seed"
git -C "$WORK/seed" -c user.name='Fakevps Test' -c user.email=fakevps@example.invalid \
  commit -q --allow-empty -m init
git clone -q --bare "$WORK/seed" "$WORK/cursor20.git"
EXTERNAL_ROOT=$(mktemp -d '/tmp/codvps cursor20.XXXXXX')
"$CODVPS_BIN" config set repo_root "$EXTERNAL_ROOT"
"$CODVPS_BIN" repo add "$WORK/cursor20.git"
"$CODVPS_BIN" config unset repo_root
[[ -d "$EXTERNAL_ROOT/cursor20/.git" && ! -e "$HOME/cursor20" ]] ||
  note "Cursor fixture was not cloned under the configured external root"

# Test: enable cursor
echo "DEBUG: Running enable cursor..."
if "$CODVPS_BIN" enable cursor >/tmp/cursor_head.log 2>&1; then
  echo "DEBUG: enable cursor succeeded"
else
  echo "DEBUG: enable cursor failed with rc=$?"
  cat /tmp/cursor_head.log >&2
  note "enable cursor failed after successful login"
fi

# Test: enable cursor creates flag
if [[ ! -f "$FLAG" ]]; then
  note "enable cursor did not create flag at $FLAG"
  echo "DEBUG: Expected flag at $FLAG"
fi

# Inspect both the fake provider's received argv and the real service's cwd.
expected_worker=$(printf '%s\t.' "$EXTERNAL_ROOT/cursor20")
for _ in {1..50}; do
  grep -Fxq -- "$expected_worker" "$WORKER_LOG" 2>/dev/null && break
  sleep 0.1
done
grep -Fxq -- "$expected_worker" "$WORKER_LOG" 2>/dev/null ||
  note "Cursor did not receive --worker-dir . in the registered external checkout"
pid=$(systemctl --user show cursor-remote@cursor20.service -p MainPID --value)
cwd=$(readlink "/proc/$pid/cwd" 2>/dev/null || true)
[[ $cwd == "$EXTERNAL_ROOT/cursor20" ]] || note "Cursor worker cwd is '$cwd', want the registered external checkout"
systemctl --user is-active --quiet cursor-remote@cursor20.service || note "external Cursor worker is not active"

# Test: disable cursor removes flag
echo "DEBUG: Running disable cursor..."
"$CODVPS_BIN" disable cursor >/dev/null 2>&1
if [[ -f "$FLAG" ]]; then
  note "disable did not remove the flag"
  echo "DEBUG: Flag still exists at $FLAG"
fi

if systemctl --user is-active --quiet cursor-remote@cursor20.service; then
  note "disable cursor left the external worker active"
fi
"$CODVPS_BIN" repo remove cursor20
[[ -d "$EXTERNAL_ROOT/cursor20/.git" ]] || note "repo remove deleted the external Cursor checkout"

[[ $FAILED -eq 0 ]] && echo "OK: all cursor tests passed" || echo "FAILED: $FAILED checks failed"
exit $FAILED
