#!/usr/bin/env bash
set -euo pipefail

# Test: invalid command usage is properly rejected
# Enabled: (CLI usage validation)

test_login_without_args() {
  local output
  set +e
  output=$("$CODVPS_BIN" login 2>&1)
  local rc=$?
  set -e

  if [[ $rc -ne 1 ]]; then
    echo "FAIL: login without args should return exit code 1, got $rc"
    return 1
  fi

  expected="usage: codvps login <claude|codex|cursor|opencode|github> [--non-interactive]"
  if ! grep -Fx "$expected" <<<"$output" >/dev/null; then
    echo "FAIL: login output does not match expected usage line"
    echo "Got: $output"
    echo "Expected: $expected"
    return 1
  fi

  echo "PASS: login without args fails with correct usage"
  return 0
}

test_repo_without_args() {
  local output
  set +e
  output=$("$CODVPS_BIN" repo 2>&1)
  local rc=$?
  set -e

  if [[ $rc -ne 1 ]]; then
    echo "FAIL: repo without args should return exit code 1, got $rc"
    return 1
  fi

  expected="usage: codvps repo <add|list|remove>"
  if ! grep -Fx "$expected" <<<"$output" >/dev/null; then
    echo "FAIL: repo output does not match expected usage line"
    echo "Got: $output"
    echo "Expected: $expected"
    return 1
  fi

  echo "PASS: repo without args fails with correct usage"
  return 0
}

test_head_without_args() {
  local output
  set +e
  output=$("$CODVPS_BIN" head 2>&1)
  local rc=$?
  set -e

  if [[ $rc -ne 1 ]]; then
    echo "FAIL: head without args should return exit code 1, got $rc"
    return 1
  fi

  expected="usage: codvps head <enable|disable|list|pair|ensure>"
  if ! grep -Fx "$expected" <<<"$output" >/dev/null; then
    echo "FAIL: head output does not match expected usage line"
    return 1
  fi

  echo "PASS: head without args fails with correct usage"
  return 0
}

# Run tests
test_login_without_args
test_repo_without_args
test_head_without_args
