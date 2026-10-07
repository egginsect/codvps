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
#   - Cursor head unit template has absolute paths (%h, %H, not ~ or %h--)

[[ -n "${HOME:-}" ]] || { echo "FAIL: HOME not set"; exit 1; }
[[ -n "${CODVPS_BIN:-}" ]] || { echo "FAIL: CODVPS_BIN not set"; exit 1; }

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

# shellcheck disable=SC2317
cleanup() {
  "$CODVPS_BIN" disable cursor >/dev/null 2>&1 || true
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
    shift
    # Parse options before "start" subcommand
    while [[ $# -gt 0 ]]; do
      case "$1" in
        --name|--worker-dir|--data-dir|--management-addr)
          shift 2
          ;;
        start)
          # Reached the start subcommand; options are in correct order
          shift
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

# Verify the unit template has absolute paths, not relative ones
# shellcheck disable=SC2088
if grep -q '~/' "$UNIT_FILE"; then
  note "cursor-remote@.service has relative paths (contains ~)"
fi

# Verify the unit has absolute ExecStart path
if ! grep -q 'ExecStart=/usr/local/bin/codvps internal shell-exec --provider cursor -- %h/.local/bin/agent' "$UNIT_FILE"; then
  note "cursor-remote@.service ExecStart must run %h/.local/bin/agent through codvps internal shell-exec"
fi

# Verify worker options come before "start" subcommand
if ! grep -q 'worker --name %H-' "$UNIT_FILE"; then
  note "cursor-remote@.service must have: worker --name %H-... (options before start)"
fi

if ! grep -q 'worker-dir %h/%i' "$UNIT_FILE"; then
  note "cursor-remote@.service must have: --worker-dir %h/%i"
fi

if ! grep -q 'data-dir %h/.local/share/cursor-agent/codvps-workers/%i' "$UNIT_FILE"; then
  note "cursor-remote@.service must have: --data-dir %h/.local/share/cursor-agent/codvps-workers/%i"
fi

if ! grep -q 'start$' "$UNIT_FILE"; then
  note "cursor-remote@.service must end with 'start' subcommand"
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

# Test: disable cursor removes flag
echo "DEBUG: Running disable cursor..."
"$CODVPS_BIN" disable cursor >/dev/null 2>&1
if [[ -f "$FLAG" ]]; then
  note "disable did not remove the flag"
  echo "DEBUG: Flag still exists at $FLAG"
fi

[[ $FAILED -eq 0 ]] && echo "OK: all cursor tests passed" || echo "FAILED: $FAILED checks failed"
exit $FAILED
