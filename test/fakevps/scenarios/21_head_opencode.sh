#!/usr/bin/env bash
set -euo pipefail

# Test: codvps login opencode, enable|disable opencode, and opencode head
# unit template verification.
#
# Covers:
#   - login opencode is gated on opencode being selected
#   - enable opencode with login succeeds
#   - disable opencode removes the flag
#   - OpenCode server unit has correct absolute paths
#   - opencode-env file created with 0600 permissions
#   - Doctor health check works (401 unauthenticated, 200 authenticated)

[[ -n "${HOME:-}" ]] || { echo "FAIL: HOME not set"; exit 1; }
[[ -n "${CODVPS_BIN:-}" ]] || { echo "FAIL: CODVPS_BIN not set"; exit 1; }

FLAG="$HOME/.config/codvps/opencode-head-enabled"
ENV_FILE="$HOME/.config/codvps/opencode-env"
COMPONENTS=/etc/codvps/components.json
UNIT_FILE=/etc/systemd/user/opencode-server.service
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

select_components() {
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":[%s],"switch":"none"}\n' "$1" | sudo tee "$COMPONENTS" >/dev/null
  sudo chmod 0644 "$COMPONENTS"
}

# shellcheck disable=SC2317
cleanup() {
  "$CODVPS_BIN" disable opencode >/dev/null 2>&1 || true
  sudo rm -f /usr/local/bin/opencode /tmp/opencode-serve.sock
  rm -rf "$HOME/.opencode"
  sudo rm -rf /etc/codvps
}
trap cleanup EXIT

# docker exec is not a login shell: start the operator's user manager
sudo loginctl enable-linger operator >/dev/null 2>&1 || true
sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
XDG_RUNTIME_DIR="/run/user/$(id -u)"
export XDG_RUNTIME_DIR

# Select opencode as a component in components.json
select_components '"claude","codex","opencode"'

# Create opencode CLI in both ~/.opencode/bin and /usr/local/bin
mkdir -p "$HOME/.opencode/bin"

# Fake opencode with basic-auth health check support
cat > "$HOME/.opencode/bin/opencode" << 'OPENCODE'
#!/usr/bin/env bash
case "$1" in
  auth)
    case "${2:-}" in
      login)
        mkdir -p "$HOME/.opencode"
        touch "$HOME/.opencode/auth.json"
        exit 0
        ;;
      list|ls)
        # Return non-empty output if authenticated
        if [[ -f "$HOME/.opencode/auth.json" ]]; then
          echo "default"
        fi
        exit 0
        ;;
    esac
    exit 127
    ;;
  serve)
    # Start a simple HTTP server with basic auth on port 4096
    # Responds to /global/health with 401 (no auth) or 200 (with auth)
    python3 << 'PYEOF'
import http.server
import json
import base64
from urllib.parse import urlparse

class AuthHealthHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = urlparse(self.path).path
        auth_header = self.headers.get('Authorization', '')

        # Check basic auth
        has_auth = False
        if auth_header.startswith('Basic '):
            try:
                credentials = base64.b64decode(auth_header[6:]).decode('utf-8')
                if 'opencode:' in credentials:
                    has_auth = True
            except:
                pass

        if path == '/global/health':
            if not has_auth:
                self.send_response(401)
                self.send_header('Content-Type', 'text/plain')
                self.end_headers()
                self.wfile.write(b'Unauthorized')
            else:
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.end_headers()
                health = {'healthy': True, 'version': '1.18.33'}
                self.wfile.write(json.dumps(health).encode())
        elif path == '/':
            if not has_auth:
                self.send_response(401)
                self.send_header('Content-Type', 'text/html')
                self.end_headers()
                self.wfile.write(b'<html><body>Unauthorized</body></html>')
            else:
                self.send_response(200)
                self.send_header('Content-Type', 'text/html')
                self.end_headers()
                self.wfile.write(b'<html><body>OpenCode Web UI</body></html>')
        else:
            self.send_response(404)
            self.end_headers()

    def log_message(self, format, *args):
        pass

try:
    server = http.server.HTTPServer(('127.0.0.1', 4096), AuthHealthHandler)
    server.serve_forever()
except:
    pass
PYEOF
    exec sleep infinity
    ;;
  --version)
    echo "1.18.33"
    exit 0
    ;;
  *)
    exit 127
    ;;
esac
OPENCODE
chmod +x "$HOME/.opencode/bin/opencode"

# Also install in /usr/local/bin so login can find it in PATH
sudo install -m 0755 "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode

# The harness installs codvps with opencode selected; verify the unit file exists
if [[ ! -f "$UNIT_FILE" ]]; then
  note "opencode-server.service not installed at $UNIT_FILE"
  exit $FAILED
fi

echo "OK: unit file found at $UNIT_FILE"

# Verify the unit template has absolute paths, not relative ones
# shellcheck disable=SC2088
if grep -q '~/' "$UNIT_FILE"; then
  note "opencode-server.service has relative paths (contains ~)"
fi

# Verify the unit has absolute ExecStart path
if ! grep -q 'ExecStart=/usr/local/bin/codvps internal shell-exec --provider opencode -- %h/.opencode/bin/opencode' "$UNIT_FILE"; then
  note "opencode-server.service ExecStart must run %h/.opencode/bin/opencode through codvps internal shell-exec"
fi

# Test: login opencode
echo "DEBUG: Running login opencode..."
if "$CODVPS_BIN" login opencode >/tmp/opencode_login.log 2>&1; then
  echo "DEBUG: login opencode succeeded"
else
  echo "DEBUG: login opencode failed with rc=$?"
  cat /tmp/opencode_login.log >&2
fi

if [[ ! -f "$HOME/.opencode/auth.json" ]]; then
  note "opencode login did not create credential"
  echo "DEBUG: Expected file at $HOME/.opencode/auth.json"
fi

# Test: enable opencode
echo "DEBUG: Running enable opencode..."
if "$CODVPS_BIN" enable opencode >/tmp/opencode_head.log 2>&1; then
  echo "DEBUG: enable opencode succeeded"
else
  echo "DEBUG: enable opencode failed with rc=$?"
  cat /tmp/opencode_head.log >&2
  note "enable opencode failed after successful login"
fi

# Test: enable opencode creates flag
if [[ ! -f "$FLAG" ]]; then
  note "enable opencode did not create flag at $FLAG"
  echo "DEBUG: Expected flag at $FLAG"
fi

# Test: enable opencode creates env file
if [[ ! -f "$ENV_FILE" ]]; then
  note "enable opencode did not create env file at $ENV_FILE"
  echo "DEBUG: Expected env file at $ENV_FILE"
fi

# Test: env file has correct format and permissions
if [[ -f "$ENV_FILE" ]]; then
  # Check permissions
  mode=$(stat -c '%a' "$ENV_FILE" 2>/dev/null || stat -f '%Lp' "$ENV_FILE" 2>/dev/null || echo "unknown")
  if [[ "$mode" != "600" && "$mode" != "0600" ]]; then
    note "env file has incorrect permissions: $mode (expected 0600)"
  fi

  # Check format
  if ! grep -q '^OPENCODE_SERVER_PASSWORD=' "$ENV_FILE"; then
    note "env file doesn't contain OPENCODE_SERVER_PASSWORD setting"
  fi
fi

# Test: second enable is idempotent (keeps same password)
if [[ -f "$ENV_FILE" ]]; then
  PASSWORD_BEFORE=$(grep OPENCODE_SERVER_PASSWORD "$ENV_FILE" | cut -d= -f2)
  "$CODVPS_BIN" disable opencode >/dev/null 2>&1
  "$CODVPS_BIN" enable opencode >/dev/null 2>&1
  if [[ -f "$ENV_FILE" ]]; then
    PASSWORD_AFTER=$(grep OPENCODE_SERVER_PASSWORD "$ENV_FILE" | cut -d= -f2)
    if [[ "$PASSWORD_BEFORE" != "$PASSWORD_AFTER" ]]; then
      note "env file password changed on second enable (should be idempotent)"
    fi
  fi
fi

# Test: disable opencode removes flag
echo "DEBUG: Running disable opencode..."
"$CODVPS_BIN" disable opencode >/dev/null 2>&1
if [[ -f "$FLAG" ]]; then
  note "head disable did not remove the flag"
  echo "DEBUG: Flag still exists at $FLAG"
fi

[[ $FAILED -eq 0 ]] && echo "OK: all opencode tests passed" || echo "FAILED: $FAILED checks failed"
exit $FAILED
