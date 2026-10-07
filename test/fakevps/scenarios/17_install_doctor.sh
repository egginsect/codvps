#!/usr/bin/env bash
set -euo pipefail

# Test: codvps install, codvps uninstall and codvps doctor,
# black-box against the product binary and the fake claude/codex.
#
# Covers (see docs/test-parity.md for the full mapping):
#   - test_user_and_repo_root                                  (tests/smoke.sh)
#   - test_install_requires_non_root_operator                  (tests/smoke.sh)
#   - test_install_is_idempotent                               (tests/smoke.sh)
#   - test_new_tooling_on_path                                 (tests/smoke.sh)
#   - test_linger_enabled                                      (tests/smoke.sh)
#   - test_installer_migrates_enabled_codex_head_into_isolation (tests/smoke.sh)
#     (now: into the unsandboxed system unit)
#   - test_doctor_credentials                                  (tests/smoke.sh)
#   - test_repo_without_claude_unit_is_not_a_doctor_error      (tests/smoke.sh)
#   - test_doctor_detects_missing_identity                     (tests/smoke.sh)
#   - test_doctor_rejects_invalid_claude_credential_source     (tests/smoke.sh)
#   - test_doctor_accepts_curated_codex_daemon                 (tests/smoke.sh)
#     (now: the shared, unsandboxed daemon)
#   - test_deselected_component_is_refused_and_doctor_skips_it (tests/smoke.sh)
#   - test_zero_heritage_path_help_works                       (tests/smoke.sh)
#   - test_bwrap_apparmor_profile                              (tests/smoke.sh)
#   - test_uninstall_and_reinstall_round_trip                  (tests/smoke.sh)
#
# Removed by owner decision (codvps no longer sandboxes Codex Remote):
# test_installer_refuses_symlinked_workspace_root,
# test_doctor_detects_codex_membership_drift_from_the_registry,
# test_doctor_detects_stale_claude_credential_mount,
# test_claude_credential_source_rejects_non_regular_files,
# test_doctor_checks_active_disabled_codex_daemon_cwd and
# test_doctor_checks_codex_alias_in_the_daemon_namespace all checked the
# mount namespace, the membership manifest or `internal codex-isolation`.
# New: test_install_retires_the_sandbox_state and
# test_doctor_warns_about_a_dangling_control_socket_link.
#
# Adaptations:
#   - `sudo codvps install` replaces `sudo bash install.sh`. Package and
#     vendor provisioning (apt, node, gh, jq, rg, pnpm, bwrap, the AppArmor
#     profile, the vendor installers) is skipped with --skip-provision, so this
#     scenario stands in root-owned stubs for the tools doctor looks for
#     and asserts on individual doctor lines, not on a fully provisioned
#     host's overall exit status.
#   - Vendor-update assertions are 19_vendor_updates.sh's. The
#     worker binary doctor checks is the Codex standalone install under
#     ~/.codex.
#   - install is the single manager of its units, binary and links
#    : a changed unit file or a regular file where a tooling link
#     belongs is replaced, and install names each one it replaces.
#     uninstall removes only the fixed declared set and keeps a regular
#     file where a link belongs.
# /usr/local/bin/codvps is the harness's regular copy of the build mounted
# at /opt/codvps-build/codvps, which reinstalls it after uninstall.

BUILD=/opt/codvps-build/codvps
WORK="$HOME/tmp/install17"
REMOTE_HOME="$HOME/.codex"
CACHE="$REMOTE_HOME/cache/fake-codex"
CRED="$HOME/.claude/.credentials.json"
STATE="$HOME/.config/codvps"
REGISTRY="$STATE/repositories"
MANIFEST="$STATE/codex-repositories"
SOCKET="$REMOTE_HOME/app-server-control/app-server-control.sock"
COMPONENTS=/etc/codvps/components.json
UNIT=codex-remote@operator.service
LEGACY_UNIT=/etc/systemd/user/codex-remote.service
DROPIN_DIR=/etc/systemd/system/codex-remote@operator.service.d
PROFILE=/etc/apparmor.d/codvps-bwrap-userns-restrict
# The literal line install appends to ~/.bashrc (expanded by the shell
# that sources it, not here).
# shellcheck disable=SC2016
PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'
STUBS="$WORK/created-stubs"
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

# run <cmd...>: capture combined output in $OUT and exit status in $RC.
run() {
  set +e
  OUT=$("$@" 2>&1)
  RC=$?
  set -e
}

codvps() { "$BUILD" "$@"; }
# --skip-provision: host provisioning (apt, vendor installers, swap, ufw;
#) needs the network and is unit-tested with fakes in
# internal/install; this scenario covers the codvps wiring.
install_as_root() { run sudo "$BUILD" install --skip-provision "$@" </dev/null; }
daemon_pid() { cat "$CACHE/remote-control.pid" 2>/dev/null || true; }
has_line() { grep -Fxq -- "$1" <<<"$OUT"; }

wait_dead() {
  local pid=$1 attempt
  for ((attempt = 1; attempt <= 30; attempt++)); do
    kill -0 "$pid" 2>/dev/null || return 0
    sleep 0.1
  done
  return 1
}

cleanup() {
  local stub
  systemctl --user disable --now codex-remote.service >/dev/null 2>&1 || true
  sudo rm -f "$LEGACY_UNIT" "$DROPIN_DIR/site-hardening.conf"
  if [[ -f $STUBS ]]; then
    while IFS= read -r stub; do
      sudo rm -f -- "$stub"
    done <"$STUBS"
  fi
  codvps head disable codex >/dev/null 2>&1 || true
  codvps head disable claude >/dev/null 2>&1 || true
}
trap cleanup EXIT

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

# stub <path> <body>: a root-owned executable standing in for a tool host
# provisioning installs, created only where none exists.
stub() {
  local path=$1 body=$2
  [[ -e $path ]] && return 0
  printf '#!/bin/sh\n%s\n' "$body" | sudo tee "$path" >/dev/null
  sudo chmod 0755 "$path"
  printf '%s\n' "$path" >>"$STUBS"
}

setup() {
  local name tool
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
  codvps head disable codex >/dev/null 2>&1 || true
  codvps head disable claude >/dev/null 2>&1 || true
  sudo chown "$(id -un):$(id -gn)" "$REMOTE_HOME" >/dev/null 2>&1 || true
  rm -rf "$WORK" "$REMOTE_HOME" "$HOME/.codex" "$HOME/.claude"
  for name in codex-one codex-two hello remove-after-move; do
    rm -rf "${HOME:?}/$name"
  done
  rm -f "$REGISTRY" "$MANIFEST"
  sudo rm -rf /etc/codvps
  mkdir -p "$WORK"
  : >"$STUBS"
  for name in codex-one codex-two hello remove-after-move; do
    fixture "$name"
  done

  # The standalone Codex and the logins host provisioning and
  # `codvps login` would leave behind.
  install -d -m 0700 "$REMOTE_HOME" "$REMOTE_HOME/packages/standalone/releases/fake" "$CACHE"
  install -m 0755 /usr/local/bin/codex "$REMOTE_HOME/packages/standalone/releases/fake/codex"
  ln -sfn releases/fake "$REMOTE_HOME/packages/standalone/current"
  printf '{"fake":true}\n' >"$REMOTE_HOME/auth.json"
  chmod 0600 "$REMOTE_HOME/auth.json"
  install -d -m 0700 "$HOME/.claude"
  printf '{"fake":"claude"}\n' >"$CRED"
  chmod 0600 "$CRED"
  for tool in uv uvx; do
    install -d -m 0755 "$HOME/.local/bin"
    printf '#!/bin/sh\nexit 0\n' >"$HOME/.local/bin/$tool"
    chmod 0755 "$HOME/.local/bin/$tool"
  done
  for tool in jq rg pnpm bwrap; do
    stub "/usr/local/bin/$tool" 'exit 0'
  done
  stub /usr/local/bin/gh 'exit 1'
  stub /usr/bin/node 'exit 0'
  if [[ ! -e $PROFILE ]]; then
    sudo install -d -m 0755 /etc/apparmor.d
    printf 'abi <abi/4.0>,\n' | sudo tee "$PROFILE" >/dev/null
    printf '%s\n' "$PROFILE" >>"$STUBS"
  fi
  git config --global user.name 'Fakevps Operator'
  git config --global user.email 'operator@example.invalid'

  # Start without linger so the install is what enables it.
  sudo loginctl disable-linger operator >/dev/null 2>&1 || true
}

test_user_and_repo_root() {
  id operator >/dev/null 2>&1 || note "the operator account is missing"
  [[ -d $HOME && "$(stat -c %U "$HOME")" == operator ]] || note "$HOME is not operator-owned"
}

test_install_requires_non_root_operator() {
  local expected='run the installer as a normal sudo-capable user via sudo, because systemd --user + linger need a login account'
  run sudo env -u SUDO_USER "$BUILD" install --components claude </dev/null
  [[ $RC -ne 0 && $OUT == *"$expected"* ]] || note "install accepted a missing SUDO_USER (rc=$RC): $OUT"
  run sudo env SUDO_USER=root "$BUILD" install --components claude </dev/null
  [[ $RC -ne 0 && $OUT == *"$expected"* ]] || note "install accepted SUDO_USER=root (rc=$RC): $OUT"
  run "$BUILD" install --components claude </dev/null
  [[ $RC -ne 0 && $OUT == *'must be run as root'* ]] || note "install ran without root (rc=$RC): $OUT"
  [[ ! -e $COMPONENTS ]] || note "a refused install wrote $COMPONENTS"
}

# Every unit file install declares (install.ProductUnits). Any one of them
# on disk is what tells install codvps was installed on this host before.
PRODUCT_UNIT_FILES=(
  /etc/systemd/user/claude-remote.slice
  /etc/systemd/user/claude-remote@.service
  /etc/systemd/system/codex-remote@.service
  /etc/systemd/user/codex-remote-watchdog.service
  /etc/systemd/user/codex-remote-watchdog.timer
  /etc/systemd/user/cursor-remote@.service
  /etc/systemd/user/opencode-server.service
)

test_install_needs_a_selection_on_a_fresh_host() {
  local path
  # The harness image ships the head units so 15/16 can run them without
  # an install; a fresh host has none, so take them away first (the
  # install below writes them again).
  sudo rm -f -- "${PRODUCT_UNIT_FILES[@]}"
  sudo systemctl daemon-reload
  install_as_root
  for path in "${PRODUCT_UNIT_FILES[@]}"; do
    [[ ! -e $path ]] || note "a refused install wrote $path"
  done
  [[ $RC -ne 0 && $OUT == *'no component selection found on a fresh host with no TTY'* ]] ||
    note "a flagless install on a fresh host did not ask for a selection (rc=$RC): $OUT"
  [[ ! -e $COMPONENTS ]] || note "a refused install wrote $COMPONENTS"
}

test_install() {
  local unit
  install_as_root --components claude,codex --switch none
  [[ $RC -eq 0 ]] || { note "install failed (rc=$RC): $OUT"; return; }
  [[ $OUT == *'codvps installation complete.'* ]] || note "install did not report completion: $OUT"
  [[ "$(cat "$COMPONENTS")" == '{"version":1,"coding_clis":["claude","codex"],"switch":"none"}' ]] ||
    note "unexpected component registry: $(cat "$COMPONENTS")"
  [[ "$(stat -c '%U:%a' "$COMPONENTS")" == root:644 ]] || note "the component registry is not root-owned 0644"
  for unit in /etc/systemd/user/claude-remote.slice /etc/systemd/user/claude-remote@.service \
    /etc/systemd/system/codex-remote@.service /etc/systemd/user/codex-remote-watchdog.service \
    /etc/systemd/user/codex-remote-watchdog.timer /usr/local/bin/codvps; do
    [[ -f $unit ]] || note "$unit is not installed"
  done
  [[ ! -e $DROPIN_DIR/workspaces.conf ]] || note "install wrote a Codex mount policy"
}

test_linger_enabled() {
  [[ "$(loginctl show-user operator -p Linger --value)" == yes ]] || note "install did not enable linger"
}

# codvps keeps no Codex state of its own: no membership manifest,
# and ~/.codex stays Codex's, untouched by install.
test_no_codex_state_installed() {
  [[ ! -e $MANIFEST ]] || note "install wrote a Codex membership manifest"
  [[ "$(stat -c '%U:%G:%a' "$STATE")" == operator:operator:700 ]] || note "$STATE is not operator:operator 700"
  [[ "$(stat -c '%U:%G:%a' "$REMOTE_HOME")" == operator:operator:700 ]] || note "$REMOTE_HOME mode was changed"
}

# The systemd units never source ~/.bashrc, so the tooling must resolve on
# the restricted unit PATH through the /usr/local/bin links; the ~/.bashrc
# line covers login shells.
test_new_tooling_on_path() {
  local tool
  for tool in uv uvx codex; do
    env PATH=/usr/local/bin:/usr/bin:/bin bash -c "command -v $tool" >/dev/null ||
      note "$tool does not resolve on the systemd unit PATH"
  done
  [[ "$(readlink /usr/local/bin/uv)" == "$HOME/.local/bin/uv" ]] || note "/usr/local/bin/uv is not the operator's uv"
  [[ "$(grep -Fxc -- "$PATH_LINE" "$HOME/.bashrc")" == 1 ]] || note "$HOME/.bashrc does not carry the PATH line exactly once"
}

# The unit files and the tooling links install owns, with their content.
installed_units() {
  sudo sha256sum /etc/systemd/user/claude-remote.slice /etc/systemd/user/claude-remote@.service \
    /etc/systemd/system/codex-remote@.service /etc/systemd/user/codex-remote-watchdog.service \
    /etc/systemd/user/codex-remote-watchdog.timer 2>&1 || true
  readlink /usr/local/bin/uv /usr/local/bin/uvx /usr/local/bin/codex || true
}

test_install_is_idempotent() {
  local before after
  before=$(installed_units)
  install_as_root
  [[ $RC -eq 0 ]] || note "a second install failed (rc=$RC): $OUT"
  [[ $OUT == *'using existing component selection from /etc/codvps/components.json: coding CLIs=claude,codex switch=none'* ]] ||
    note "a flagless reinstall did not keep the recorded selection: $OUT"
  ! grep -q '^replacing /etc/systemd/' <<<"$OUT" || note "a second install replaced an unchanged unit file: $OUT"
  after=$(installed_units)
  [[ $before == "$after" ]] || note "a second install changed the unit files or links"
  [[ "$(grep -Fxc -- "$PATH_LINE" "$HOME/.bashrc")" == 1 ]] || note "a second install duplicated the $HOME/.bashrc PATH line"
}

# install is the single manager of its unit files: one that was edited is
# rewritten, and install names it.
test_install_replaces_a_changed_unit_file() {
  local slice=/etc/systemd/user/claude-remote.slice before
  before=$(sudo sha256sum "$slice")
  printf '# site policy\n' | sudo tee -a "$slice" >/dev/null
  install_as_root
  [[ $RC -eq 0 ]] || note "install over a changed unit file failed (rc=$RC): $OUT"
  [[ $OUT == *"replacing $slice: it differs from what codvps installs there"* ]] ||
    note "install did not name the unit file it replaced: $OUT"
  [[ "$(sudo sha256sum "$slice")" == "$before" ]] || note "install did not restore $slice"
}

# What the retired sandbox left behind is removed on install: the generated
# workspaces.conf drop-in (and its directory), the membership manifest, and a
# control-socket link dangling into the old unit's private /tmp. A drop-in the
# operator wrote stays.
test_install_retires_the_sandbox_state() {
  sudo install -d -m 0755 "$DROPIN_DIR"
  printf '[Service]\nBindPaths=%s/codex-one\nTemporaryFileSystem=%s/codex-one/.claude/worktrees:ro\n' "$HOME" "$HOME" |
    sudo tee "$DROPIN_DIR/workspaces.conf" >/dev/null
  printf 'codex-one\n' >"$MANIFEST"
  chmod 0600 "$MANIFEST"
  install -d -m 0700 "$(dirname -- "$SOCKET")"
  ln -sfn /tmp/systemd-private-gone/tmp/app-server-control.sock "$SOCKET"

  install_as_root
  [[ $RC -eq 0 ]] || { note "install over the retired sandbox state failed (rc=$RC): $OUT"; return; }
  [[ ! -e $DROPIN_DIR/workspaces.conf && ! -e $DROPIN_DIR ]] || note "install left the generated workspaces.conf drop-in"
  [[ ! -e $MANIFEST ]] || note "install left the Codex membership manifest"
  [[ ! -L $SOCKET ]] || note "install left the dangling control socket link"

  # Not ours: a drop-in of the operator's, even at the same path.
  sudo install -d -m 0755 "$DROPIN_DIR"
  printf '[Service]\nBindPaths=%s/codex-one\nPrivateTmp=yes\n' "$HOME" | sudo tee "$DROPIN_DIR/workspaces.conf" >/dev/null
  install_as_root
  [[ $RC -eq 0 && -f $DROPIN_DIR/workspaces.conf ]] || note "install removed a workspaces.conf it did not generate (rc=$RC): $OUT"
  sudo rm -rf "$DROPIN_DIR"
  sudo systemctl daemon-reload
}

test_installer_migrates_enabled_codex_head_to_the_system_unit() {
  local old_pid new_pid
  # A home-rooted --user head from before the system unit.
  sudo tee "$LEGACY_UNIT" >/dev/null <<'UNIT'
[Unit]
Description=Legacy home-rooted Codex Remote

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=%h
Environment=CODEX_HOME=%h/.codex
ExecStart=/usr/local/bin/codex remote-control start --json
ExecStop=/usr/local/bin/codex remote-control stop --json

[Install]
WantedBy=default.target
UNIT
  systemctl --user daemon-reload
  systemctl --user enable --now codex-remote.service
  old_pid=$(cat "$HOME/.codex/cache/fake-codex/remote-control.pid" 2>/dev/null || true)
  [[ $old_pid =~ ^[1-9][0-9]*$ ]] || { note "the legacy Codex head did not start"; return; }

  install_as_root
  [[ $RC -eq 0 ]] || { note "the migrating install failed (rc=$RC): $OUT"; return; }
  systemctl is-enabled --quiet "$UNIT" || note "the Codex head is not enabled after migration"
  systemctl is-active --quiet "$UNIT" || note "the Codex head is not active after migration"
  new_pid=$(daemon_pid)
  [[ $new_pid =~ ^[1-9][0-9]*$ && $new_pid != "$old_pid" ]] || note "no new daemon after migration"
  wait_dead "$old_pid" || note "the legacy Codex daemon survived the migration"
  [[ ! -e $LEGACY_UNIT ]] || note "the legacy --user unit file survived"
  [[ -S $SOCKET && ! -L $SOCKET ]] || note "the migrated daemon's control socket is not a plain socket"
  systemctl --user stop codex-remote-watchdog.timer >/dev/null 2>&1 || true
}

test_doctor_accepts_curated_codex_daemon() {
  run codvps doctor
  has_line 'PASS: installed Codex unit runs the official daemon lifecycle' || note "doctor did not accept the Codex unit: $OUT"
  has_line 'PASS: Codex Remote head is enabled and its daemon is reachable' || note "doctor did not reach the shared daemon: $OUT"
  [[ $OUT == *"PASS: Codex worker binary is present: $REMOTE_HOME/packages/standalone/releases/fake/codex"* ]] ||
    note "doctor did not verify the standalone worker binary: $OUT"
  [[ $OUT != *'WARN: '*'dangling symlink'* ]] || note "doctor warned about a control socket link that is not dangling: $OUT"
  # An ambient CODEX_HOME must not redirect the health checks.
  run env CODEX_HOME=/tmp/wrong-codex-home "$BUILD" doctor
  has_line 'PASS: Codex Remote head is enabled and its daemon is reachable' || note "an ambient CODEX_HOME redirected doctor: $OUT"
}

# The retired sandboxed unit left the control socket a symlink into its
# private /tmp, which the host codex CLI cannot follow.
test_doctor_warns_about_a_dangling_control_socket_link() {
  local target
  target=$(readlink -f -- "$SOCKET")
  sudo systemctl stop "$UNIT"
  rm -f -- "$SOCKET"
  ln -s /tmp/systemd-private-gone/tmp/app-server-control.sock "$SOCKET"
  run codvps doctor
  has_line "WARN: $SOCKET is a dangling symlink left by the retired sandboxed Codex unit, so the codex CLI cannot reach the daemon; run codvps enable codex" ||
    note "doctor did not warn about a dangling control socket link (target $target): $OUT"
  run codvps enable codex
  [[ $RC -eq 0 ]] || note "head enable codex over a dangling link failed (rc=$RC): $OUT"
  systemctl --user stop codex-remote-watchdog.timer >/dev/null 2>&1 || true
  [[ -S $SOCKET && ! -L $SOCKET ]] || note "head enable codex left the control socket link"
  run codvps doctor
  ! grep -q 'dangling symlink' <<<"$OUT" || note "doctor still warns after head enable codex: $OUT"
}

test_bwrap_apparmor_profile() {
  run codvps doctor
  has_line 'PASS: bubblewrap AppArmor profile is installed' || note "doctor did not find $PROFILE: $OUT"
  sudo mv "$PROFILE" "$PROFILE.bak"
  run codvps doctor
  sudo mv "$PROFILE.bak" "$PROFILE"
  if [[ $RC -eq 0 ]] || ! has_line 'FAIL: bubblewrap AppArmor profile is missing'; then
    note "doctor accepted a missing AppArmor profile (rc=$RC): $OUT"
  fi
}

test_zero_heritage_path_help_works() {
  run codvps help
  [[ $RC -eq 0 ]] || note "help failed (rc=$RC)"
  run codvps doctor
  ! grep -Eiq '^(WARN|FAIL).*(heritage|personal config|CLAUDE\.md)' <<<"$OUT" ||
    note "doctor reported an absent personal config as a problem: $OUT"
}

test_doctor_detects_missing_identity() {
  mv "$HOME/.gitconfig" "$HOME/.gitconfig.bak"
  run codvps doctor
  mv "$HOME/.gitconfig.bak" "$HOME/.gitconfig"
  [[ $RC -eq 1 && $OUT == *'FAIL: git identity is incomplete; run: codvps login github'* ]] ||
    note "doctor accepted a missing git identity (rc=$RC): $OUT"
}

test_doctor_credentials() {
  run codvps doctor
  has_line 'PASS: Claude OAuth credential file is present' || note "doctor did not see the Claude credential: $OUT"
  has_line 'PASS: Codex OAuth credential file is present' || note "doctor did not see the Codex credential: $OUT"
  [[ $OUT != *'{"fake"'* ]] || note "doctor printed a credential"
}

test_doctor_rejects_invalid_claude_credential_source() {
  mv "$CRED" "$CRED.bak"
  mkdir "$CRED"
  run codvps doctor
  rmdir "$CRED"
  mv "$CRED.bak" "$CRED"
  [[ $RC -eq 1 && $OUT == *'Claude OAuth credential source must be a real regular file when present'* ]] ||
    note "doctor accepted a directory Claude credential (rc=$RC): $OUT"
}

# The unit's root ExecStartPre validation refuses a credential that is a
# directory or a symlink, so such a source is never bound into Codex.
# A daemon the operator started with the codex CLI (outside the enabled
# unit) is only a warning; enabling the head then works with the daemon
# Codex is already running.
test_doctor_flags_a_daemon_outside_the_unit() {
  sudo systemctl disable --now "$UNIT"
  (cd "$HOME" && codex remote-control start --json >/dev/null)
  run codvps doctor
  has_line 'WARN: Codex Remote daemon is running outside the enabled codvps unit' || note "doctor did not flag the unmanaged daemon: $OUT"

  run codvps enable codex
  [[ $RC -eq 0 ]] || note "head enable codex with an existing daemon failed (rc=$RC): $OUT"
  systemctl --user stop codex-remote-watchdog.timer >/dev/null 2>&1 || true
  systemctl is-active --quiet "$UNIT" || note "head enable codex did not start the unit"
  CODEX_HOME="$REMOTE_HOME" codex app-server daemon version >/dev/null 2>&1 || note "the Codex daemon is unreachable after head enable codex"
}

test_repo_without_claude_unit_is_not_a_doctor_error() {
  systemctl --user enable claude-remote@missing-dir.service >/dev/null 2>&1
  run codvps doctor
  systemctl --user disable claude-remote@missing-dir.service >/dev/null 2>&1 || true
  [[ $OUT == *'WARN: enabled or running unit claude-remote@missing-dir.service has no repository at '"$HOME"'/missing-dir'* ]] ||
    note "doctor did not warn about a unit without a repository: $OUT"
  ! grep -q '^FAIL:.*missing-dir' <<<"$OUT" || note "doctor failed on a unit without a repository: $OUT"
}

test_deselected_component_is_refused_and_doctor_skips_it() {
  local before
  before=$(cat "$COMPONENTS")
  printf '{"version":1,"coding_clis":["claude"],"switch":"none"}\n' | sudo tee "$COMPONENTS" >/dev/null
  run codvps enable codex
  [[ $RC -ne 0 && $OUT == *'codex is not a selected component'* ]] || note "head enable codex was not refused (rc=$RC): $OUT"
  run codvps doctor
  printf '%s\n' "$before" | sudo tee "$COMPONENTS" >/dev/null
  has_line 'WARN: codex is not a selected component; skipping its checks' || note "doctor did not skip codex: $OUT"
  ! grep -qE '^FAIL:.*[Cc]odex' <<<"$OUT" || note "doctor failed a deselected component: $OUT"
}

fingerprint() {
  local path
  for path in /usr/local/bin/codvps /usr/local/bin/uv /usr/local/bin/uvx \
    /etc/systemd/user/claude-remote@.service /etc/systemd/user/claude-remote.slice \
    /etc/systemd/system/codex-remote@.service /etc/systemd/user/codex-remote-watchdog.service \
    /etc/systemd/user/codex-remote-watchdog.timer "$DROPIN_DIR" "$DROPIN_DIR/workspaces.conf" \
    "$COMPONENTS" "$STATE" "$REGISTRY" "$REMOTE_HOME/auth.json" "$CRED" \
    "$HOME/codex-one/.git"; do
    if [[ -L $path ]]; then
      printf '%s symlink\n' "$path"
    elif [[ -d $path ]]; then
      printf '%s dir\n' "$path"
    elif [[ -e $path ]]; then
      printf '%s file\n' "$path"
    else
      printf '%s absent\n' "$path"
    fi
  done
}

test_uninstall_and_reinstall_round_trip() {
  local before after stray=/usr/local/bin/uv foreign="$DROPIN_DIR/site-hardening.conf"
  sudo install -d -m 0755 "$DROPIN_DIR"
  # Uninstall must keep the operator's registry and checkouts. The old
  # migration test registered codex-one; it no longer does, so do it here.
  run codvps repo add "$WORK/codex-one.git"
  [[ $RC -eq 0 ]] || note "repo add codex-one failed (rc=$RC): $OUT"
  sudo rm -f "$stray"
  printf '#!/bin/sh\n# root-owned file where the uv link belongs\nexit 0\n' | sudo tee "$stray" >/dev/null
  sudo chmod 0755 "$stray"
  printf '[Service]\nNoNewPrivileges=yes\n' | sudo tee "$foreign" >/dev/null
  # A workspaces.conf an earlier codvps generated that install never retired.
  printf '[Service]\nBindPaths=%s/codex-one\n' "$HOME" | sudo tee "$DROPIN_DIR/workspaces.conf" >/dev/null

  before=$(fingerprint)
  run sudo "$BUILD" uninstall --dry-run
  [[ $RC -eq 0 && $OUT == *'DRY-RUN: no changes will be made.'* && $OUT == *'DRY-RUN complete; nothing was removed.'* ]] ||
    note "uninstall --dry-run failed (rc=$RC): $OUT"
  after=$(fingerprint)
  [[ $before == "$after" ]] || note "uninstall --dry-run changed state: $(diff <(printf '%s\n' "$before") <(printf '%s\n' "$after") || true)"

  run sudo "$BUILD" uninstall
  [[ $RC -eq 0 && $OUT == *'Uninstall complete.'* ]] || note "uninstall failed (rc=$RC): $OUT"
  [[ $OUT == *'skipping /usr/local/bin/uv: not a symlink, so it is not the link codvps makes'* ]] ||
    note "uninstall did not report skipping the regular file at the uv link: $OUT"
  for path in /usr/local/bin/codvps /usr/local/bin/uvx /etc/systemd/user/claude-remote@.service \
    /etc/systemd/user/claude-remote.slice /etc/systemd/system/codex-remote@.service \
    /etc/systemd/user/codex-remote-watchdog.timer "$DROPIN_DIR/workspaces.conf"; do
    [[ ! -e $path && ! -L $path ]] || note "uninstall left $path behind"
  done
  [[ -f $foreign ]] || note "uninstall removed a foreign Codex drop-in"
  grep -Fq 'where the uv link belongs' "$stray" || note "uninstall removed the regular file at the uv link"
  ! systemctl is-active --quiet "$UNIT" || note "uninstall left the Codex head running"
  for path in "$REGISTRY" "$REMOTE_HOME/auth.json" "$CRED" "$COMPONENTS" "$HOME/codex-one/.git"; do
    [[ -e $path ]] || note "uninstall removed $path"
  done

  before=$(fingerprint)
  run sudo "$BUILD" uninstall
  [[ $RC -eq 0 && $OUT == *'/usr/local/bin/codvps: already absent.'* && $OUT == *'/etc/systemd/user/claude-remote.slice: already absent.'* ]] ||
    note "a second uninstall was not a clean no-op (rc=$RC): $OUT"
  after=$(fingerprint)
  [[ $before == "$after" ]] || note "a second uninstall changed state"

  install_as_root
  [[ $RC -eq 0 ]] || { note "reinstall failed (rc=$RC): $OUT"; return; }
  [[ -x /usr/local/bin/codvps && -f /etc/systemd/system/codex-remote@.service && -f /etc/systemd/user/claude-remote@.service ]] ||
    note "reinstall did not restore the binary and units"
  [[ $OUT == *"replacing /usr/local/bin/uv with the link to $HOME/.local/bin/uv"* ]] ||
    note "reinstall did not name the file it replaced with the uv link: $OUT"
  [[ "$(readlink "$stray")" == "$HOME/.local/bin/uv" ]] || note "reinstall did not restore the uv link"
  run codvps enable codex
  [[ $RC -eq 0 ]] || note "head enable codex after reinstall failed (rc=$RC): $OUT"
  systemctl --user stop codex-remote-watchdog.timer >/dev/null 2>&1 || true
  systemctl is-active --quiet "$UNIT" || note "the Codex head did not come back after reinstall"
  sudo rm -f "$foreign"
}

setup
test_user_and_repo_root
test_install_requires_non_root_operator
test_install_needs_a_selection_on_a_fresh_host
test_install
test_linger_enabled
test_no_codex_state_installed
test_new_tooling_on_path
test_install_is_idempotent
test_install_replaces_a_changed_unit_file
test_install_retires_the_sandbox_state
test_installer_migrates_enabled_codex_head_to_the_system_unit
test_doctor_accepts_curated_codex_daemon
test_doctor_warns_about_a_dangling_control_socket_link
test_bwrap_apparmor_profile
test_zero_heritage_path_help_works
test_doctor_detects_missing_identity
test_doctor_credentials
test_doctor_rejects_invalid_claude_credential_source
test_doctor_flags_a_daemon_outside_the_unit
test_repo_without_claude_unit_is_not_a_doctor_error
test_deselected_component_is_refused_and_doctor_skips_it
test_uninstall_and_reinstall_round_trip

if [[ $FAILED -ne 0 ]]; then
  exit 1
fi
echo "PASS: install, uninstall and doctor"
