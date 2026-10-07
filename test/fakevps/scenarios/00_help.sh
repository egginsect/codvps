#!/usr/bin/env bash
set -euo pipefail

# Test: codvps help displays usage information
# Enabled: (CLI help output)

test_help_displays_usage() {
  local output
  output=$("$CODVPS_BIN" help)
  if [[ ! "$output" =~ "Usage: codvps" ]]; then
    echo "FAIL: help output does not contain 'Usage: codvps'"
    return 1
  fi
  if [[ ! "$output" =~ "Commands:" ]]; then
    echo "FAIL: help output does not contain 'Commands:'"
    return 1
  fi
  if [[ "$output" =~ "head enable" ]]; then
    echo "FAIL: help output contains 'head enable' (should use 'enable' instead)"
    return 1
  fi
  if [[ "$output" != *"  enable <claude|"* || "$output" != *"  disable <claude|"* ]]; then
    echo "FAIL: help output does not contain the top-level 'enable|disable <cli>' forms"
    return 1
  fi
  echo "PASS: help displays usage information"
  return 0
}

test_help_with_args_fails() {
  if "$CODVPS_BIN" help extra-arg 2>/dev/null; then
    echo "FAIL: help with extra args should fail"
    return 1
  fi
  echo "PASS: help with extra args properly fails"
  return 0
}

# Run tests
test_help_displays_usage
test_help_with_args_fails
