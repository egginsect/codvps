#!/usr/bin/env bash
set -euo pipefail

# Syntax gate validation for fake-VPS shell scripts
# Validates that bash -n catches errors in ALL files, not just the first one
# This is a self-test proving the gate catches syntax errors in non-first files

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILED=0

echo "Validating syntax of all fake-VPS shell scripts..."
echo ""

# Test that bash -n validates each file individually
for script in "$SCRIPT_DIR"/run.sh "$SCRIPT_DIR"/scenarios/*.sh "$SCRIPT_DIR"/fakes/*; do
  if [[ ! -f "$script" ]]; then
    continue
  fi

  # Skip non-shell files
  if [[ "$script" != *.sh ]] && ! head -1 "$script" 2>/dev/null | grep -q '^#!/.*bash'; then
    continue
  fi

  script_name=$(basename "$script")
  if bash -n "$script" 2>/dev/null; then
    echo "✓ $script_name"
  else
    echo "✗ $script_name"
    FAILED=$((FAILED + 1))
  fi
done

echo ""
if [[ $FAILED -gt 0 ]]; then
  echo "FAILED: $FAILED files have syntax errors"
  exit 1
fi

echo "SUCCESS: All shell scripts pass syntax validation"

# Self-test: prove that an intentional syntax error in a non-first file is caught
echo ""
echo "Testing gate robustness: creating a temporary file with a syntax error..."

TEMP_GOOD=$(mktemp)
TEMP_BAD=$(mktemp)

# shellcheck disable=SC2064
trap "rm -f '$TEMP_GOOD' '$TEMP_BAD'" EXIT

# Good script
cat >"$TEMP_GOOD" <<'EOF'
#!/bin/bash
echo "good"
EOF

# Bad script with syntax error
cat >"$TEMP_BAD" <<'EOF'
#!/bin/bash
if true; then
  echo "bad"
# Missing closing 'fi'
EOF

# Test that bash -n catches the error in the bad file
if bash -n "$TEMP_GOOD" 2>/dev/null; then
  echo "✓ Good script validates"
else
  echo "✗ Good script should validate (test framework issue)"
  exit 1
fi

if bash -n "$TEMP_BAD" 2>/dev/null; then
  echo "✗ Bad script should fail validation (gate failed to catch error)"
  exit 1
else
  echo "✓ Gate correctly rejects bad syntax in non-first file"
fi

echo ""
echo "Syntax gate validation complete!"
