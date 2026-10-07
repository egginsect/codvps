#!/usr/bin/env bash
set -euo pipefail

# Test: codvps components list|status against a root-owned registry
#
# Covers (see docs/test-parity-components.md for the full mapping):
#   - test_components_missing_registry_nothing_selected  (tests/components.sh)
#   - test_components_missing_registry_switch_none       (tests/components.sh)
#   - test_components_list_and_status_show_full_selection (tests/smoke.sh)
#   - test_components_corrupt_registry_loud_failure       (tests/components.sh)
#   - test_components_selected_reflects_write             (tests/components.sh)
#   - test_components_selected_reflects_rewrite           (tests/components.sh)
#   - test_components_validate_clis_unknown_dies          (tests/components.sh)
#   - test_components_validate_switch_cc_switch_reserved  (tests/components.sh)
#   - test_components_validate_switch_unknown_dies        (tests/components.sh)
#   - the capability columns of test_components_capability_* (tests/components.sh)
#
# Writes /etc/codvps/components.json as root (via sudo), reads it back as
# the unprivileged operator, and removes /etc/codvps when done. The
# selection-validation cases run `sudo codvps install` with an invalid
# --components/--switch value: install refuses those before its first host
# mutation, so they never install anything. The valid-value rows of the
# reference's validator, the registry file mode and the fresh-host
# selection gate are covered by Go unit tests and 17_install_doctor.sh (see
# docs/test-parity-components.md).

REGISTRY=/etc/codvps/components.json
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

cleanup() { sudo rm -rf /etc/codvps; }
trap cleanup EXIT

capability_suffix() {
  # Mirrors internal/components.CapabilityTable exactly, one row per
  # component in `codvps components status`'s fixed column order.
  case "$1" in
    claude|codex)
      echo "install=yes configure=yes auth=yes update=yes remove=no service=yes native_remote=yes" ;;
    cursor)
      echo "install=yes configure=yes auth=yes update=yes remove=no service=yes native_remote=yes" ;;
    opencode)
      echo "install=yes configure=yes auth=yes update=yes remove=no service=yes native_remote=no" ;;
    cc-switch)
      echo "install=planned configure=planned auth=planned update=planned remove=planned service=planned native_remote=planned" ;;
  esac
}

expected_status_row() {
  printf '%-10s %-25s %s' "$1" "$2" "$(capability_suffix "$1")"
}

setup() {
  sudo rm -rf /etc/codvps
}

test_missing_registry() {
  local out expected_header
  out=$("$CODVPS_BIN" components list)
  if ! grep -qFx 'coding_clis: none' <<<"$out"; then
    note "components list with no registry did not report coding_clis: none: $out"
  fi
  if ! grep -qFx 'switch: none' <<<"$out"; then
    note "components list with no registry did not report switch: none: $out"
  fi

  out=$("$CODVPS_BIN" components status)
  expected_header="$(printf '%-10s %-25s %s' COMPONENT STATE 'CAPABILITIES(install/configure/auth/update/remove/service/native_remote)')"
  if ! grep -qFx "$expected_header" <<<"$out"; then
    note "components status header did not match: $out"
  fi
  for name in claude codex cursor opencode; do
    if ! grep -qFx "$(expected_status_row "$name" not-selected)" <<<"$out"; then
      note "components status did not report $name as not-selected with no registry: $out"
    fi
  done
  if ! grep -qFx "$(expected_status_row cc-switch unsupported)" <<<"$out"; then
    note "components status did not report cc-switch as unsupported: $out"
  fi
}

# The reference's validator dies on these values; codvps install refuses
# them the same way before it writes anything.
test_install_rejects_invalid_selection() {
  expect_install_refusal --components claude,notacli \
    'unknown coding CLI in --components: notacli (known: claude codex cursor opencode)'
  expect_install_refusal --switch cc-switch \
    'cc-switch is planned but unsupported in this slice; use --switch none'
  expect_install_refusal --switch bogus \
    'unknown --switch value: bogus (known: none)'
}

# expect_install_refusal <flag> <value> <message>
expect_install_refusal() {
  local out rc
  set +e
  out=$(sudo "$CODVPS_BIN" install --skip-provision "$1" "$2" </dev/null 2>&1)
  rc=$?
  set -e
  if [[ $rc -ne 1 ]]; then
    note "install $1 $2 did not exit 1 (got $rc): $out"
  fi
  if ! grep -qFx "codvps: $3" <<<"$out"; then
    note "install $1 $2 did not refuse with '$3': $out"
  fi
  if sudo test -e "$REGISTRY"; then
    note "install $1 $2 wrote $REGISTRY despite refusing the selection"
  fi
}

# component_selected/switch_selected after writing claude,codex + switch none.
test_full_selection() {
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":["claude","codex"],"switch":"none"}' | sudo tee "$REGISTRY" >/dev/null
  sudo chmod 0644 "$REGISTRY"

  local out name
  out=$("$CODVPS_BIN" components list)
  if ! grep -qFx 'coding_clis: claude,codex' <<<"$out"; then
    note "components list did not report the claude,codex selection: $out"
  fi
  if ! grep -qFx 'switch: none' <<<"$out"; then
    note "components list did not report switch: none: $out"
  fi

  out=$("$CODVPS_BIN" components status)
  # Selected components report an install/credential state that depends on
  # the fakes present, but never not-selected.
  for name in claude codex; do
    if grep -qFx "$(expected_status_row "$name" not-selected)" <<<"$out"; then
      note "components status reports selected $name as not-selected: $out"
    fi
  done
  if ! grep -qFx "$(expected_status_row cc-switch unsupported)" <<<"$out"; then
    note "components status did not report cc-switch as unsupported: $out"
  fi
}

test_claude_only_selection() {
  # Rewrites test_full_selection's registry: codex is deselected, claude
  # stays selected.
  sudo mkdir -p /etc/codvps
  printf '{"version":1,"coding_clis":["claude"],"switch":"none"}' | sudo tee "$REGISTRY" >/dev/null
  sudo chmod 0644 "$REGISTRY"

  local out
  out=$("$CODVPS_BIN" components list)
  if ! grep -qFx 'coding_clis: claude' <<<"$out"; then
    note "components list did not report the claude-only selection: $out"
  fi
  if ! grep -qFx 'switch: none' <<<"$out"; then
    note "components list did not report switch: none for the claude-only selection: $out"
  fi

  out=$("$CODVPS_BIN" components status)
  # claude is selected; whether it further reports ready/installed-not-configured
  # depends on the fake claude binary and operator credential state (see the
  # final report's assumptions), but it must never be not-selected.
  if grep -qFx "$(expected_status_row claude not-selected)" <<<"$out"; then
    note "components status still reports claude as not-selected after selecting it: $out"
  fi
  if ! grep -qFx "$(expected_status_row codex not-selected)" <<<"$out"; then
    note "components status did not report codex as not-selected: $out"
  fi
  if ! grep -qFx "$(expected_status_row opencode not-selected)" <<<"$out"; then
    note "components status did not report opencode as not-selected: $out"
  fi
  if ! grep -qFx "$(expected_status_row cc-switch unsupported)" <<<"$out"; then
    note "components status did not report cc-switch as unsupported: $out"
  fi
}

test_corrupt_registry() {
  printf 'not valid json' | sudo tee "$REGISTRY" >/dev/null
  sudo chmod 0644 "$REGISTRY"

  local rc
  set +e
  "$CODVPS_BIN" components list >/dev/null 2>&1
  rc=$?
  set -e
  if [[ $rc -eq 0 ]]; then
    note "components list on a corrupt registry did not exit non-zero"
  fi

  set +e
  "$CODVPS_BIN" components status >/dev/null 2>&1
  rc=$?
  set -e
  if [[ $rc -eq 0 ]]; then
    note "components status on a corrupt registry did not exit non-zero"
  fi
}

setup
test_missing_registry
test_install_rejects_invalid_selection
test_full_selection
test_claude_only_selection
test_corrupt_registry

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more components assertions failed"
  exit 1
fi
echo "PASS: components list/status report the reference registry states, install refuses invalid selections, and corruption fails closed"
