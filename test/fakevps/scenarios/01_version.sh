#!/usr/bin/env bash
set -euo pipefail

# Test: codvps version displays version information
# Enabled: (CLI version output)

test_version_displays_info() {
  local output
  output=$("$CODVPS_BIN" version)
  if [[ ! "$output" =~ "codvps version" ]]; then
    echo "FAIL: version output does not contain 'codvps version'"
    return 1
  fi
  if [[ ! "$output" =~ "commit" ]]; then
    echo "FAIL: version output does not contain 'commit'"
    return 1
  fi
  echo "PASS: version displays version information"
  return 0
}

test_version_with_args_fails() {
  if "$CODVPS_BIN" version extra-arg 2>/dev/null; then
    echo "FAIL: version with extra args should fail"
    return 1
  fi
  echo "PASS: version with extra args properly fails"
  return 0
}

# Run tests
test_version_displays_info
test_version_with_args_fails
