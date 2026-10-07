#!/usr/bin/env bash
set -euo pipefail

# Test: unknown commands are properly rejected
# Enabled: (CLI error handling)

test_unknown_command_fails() {
  local output
  output=$("$CODVPS_BIN" unknown-command 2>&1 || true)
  if [[ ! "$output" =~ "unknown command" ]]; then
    echo "FAIL: unknown command should output 'unknown command'"
    return 1
  fi
  echo "PASS: unknown command properly fails"
  return 0
}

test_unknown_command_exit_code() {
  local exit_code
  set +e
  "$CODVPS_BIN" unknown-command >/dev/null 2>&1
  exit_code=$?
  set -e
  if [[ $exit_code -ne 1 ]]; then
    echo "FAIL: unknown command should return exit code 1, got $exit_code"
    return 1
  fi
  echo "PASS: unknown command returns exit code 1"
  return 0
}

# Run tests
test_unknown_command_fails
test_unknown_command_exit_code
