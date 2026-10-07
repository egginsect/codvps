#!/usr/bin/env bash
set -euo pipefail

# Black-box test runner for the fake-VPS harness
# Builds the codvps binary, starts a container, runs scenario scripts, and collects results

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
BINARY_NAME="codvps"
IMAGE_NAME="${CODVPS_FAKEVPS_IMAGE:-codvps-fakevps-test}"
CONTAINER_NAME="codvps-fakevps-$(date +%s)-$$"
ARTIFACTS_DIR="${SCRIPT_DIR}/logs"
FAILED=0
PASSED=0
BUILD_DIR=""

# Cleanup function to always run
cleanup() {
  local exit_code=$?

  # Collect diagnostics if container exists
  if docker ps -a 2>/dev/null | grep -q "$CONTAINER_NAME"; then
    mkdir -p "$ARTIFACTS_DIR"
    docker logs "$CONTAINER_NAME" >"$ARTIFACTS_DIR/container.log" 2>&1 || true
    docker exec "$CONTAINER_NAME" journalctl -b --no-pager 2>/dev/null >"$ARTIFACTS_DIR/journalctl.log" 2>&1 || true
    docker exec "$CONTAINER_NAME" systemctl --failed 2>/dev/null >"$ARTIFACTS_DIR/systemctl-failed.log" 2>&1 || true
  fi

  # Always clean up the container
  if docker ps -a 2>/dev/null | grep -q "$CONTAINER_NAME"; then
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  fi

  # Clean up temporary build directory
  if [[ -n "$BUILD_DIR" && -d "$BUILD_DIR" ]]; then
    rm -rf "$BUILD_DIR"
  fi

  exit "$exit_code"
}

trap cleanup EXIT

# Step 1: Build the static codvps binary to absolute path
echo "Building codvps binary..."
BUILD_DIR=$(mktemp -d)
BINARY_PATH="$BUILD_DIR/$BINARY_NAME"
cd "$PROJECT_ROOT"
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X github.com/egginsect/codvps/internal/buildinfo.Version=test \
    -X github.com/egginsect/codvps/internal/buildinfo.Commit=test \
    -X github.com/egginsect/codvps/internal/buildinfo.BuildDate=test" \
  -o "$BINARY_PATH" ./cmd/codvps

# Step 2: Build the Docker image
echo "Building Docker image..."
docker build -t "$IMAGE_NAME" -f "$SCRIPT_DIR/Dockerfile" "$PROJECT_ROOT"

# Step 3: Start the container with proper systemd support
echo "Starting container $CONTAINER_NAME..."
docker run -d \
  --name "$CONTAINER_NAME" \
  --privileged \
  --cgroupns=host \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  --tmpfs /run \
  --tmpfs /run/lock \
  -v "$BINARY_PATH:/opt/codvps-build/$BINARY_NAME:ro" \
  "$IMAGE_NAME" \
  >/dev/null

# Step 4: Wait for systemd to be ready
echo "Waiting for systemd to be ready..."
for attempt in {1..30}; do
  if docker exec "$CONTAINER_NAME" systemctl is-system-running --wait 2>/dev/null | grep -q "running\|degraded"; then
    break
  fi
  if [[ $attempt -eq 30 ]]; then
    echo "Timeout waiting for systemd" >&2
    exit 1
  fi
  sleep 1
done

# Step 5: Install the binary as a regular root-owned file. The build is
# mounted read-only under /opt/codvps-build; /usr/local/bin/codvps must be
# an ordinary file because 17_install_doctor.sh uninstalls and reinstalls it.
docker exec "$CONTAINER_NAME" install -m 0755 "/opt/codvps-build/$BINARY_NAME" "/usr/local/bin/$BINARY_NAME"

# Step 5b: Copy scenario scripts into container
echo "Setting up test scenarios..."
docker cp "$SCRIPT_DIR/scenarios" "$CONTAINER_NAME":/opt/scenarios

# Step 6: Run scenario scripts
echo "Running scenarios..."
SCENARIO_DIR="/opt/scenarios"

for scenario in "$SCRIPT_DIR/scenarios"/*.sh; do
  if [[ ! -f "$scenario" ]]; then
    continue
  fi

  scenario_name=$(basename "$scenario" .sh)
  if [[ "$scenario_name" == "SKIPPED_SCENARIOS" ]]; then
    continue
  fi

  printf "  %-30s " "$scenario_name"
  log_file="$ARTIFACTS_DIR/$scenario_name.log"
  mkdir -p "$ARTIFACTS_DIR"

  if docker exec -u operator -e CODVPS_BIN="/usr/local/bin/$BINARY_NAME" "$CONTAINER_NAME" bash "$SCENARIO_DIR/$scenario_name.sh" >"$log_file" 2>&1; then
    echo "PASS"
    PASSED=$((PASSED + 1))
  else
    echo "FAIL"
    FAILED=$((FAILED + 1))
    echo "--- Output from $scenario_name ---" >&2
    cat "$log_file" >&2
    echo "---" >&2
  fi
done

# Step 7: Print all skipped scenarios from test-parity.md
echo ""
echo "Reference scenarios not ported by design (reasons in scenarios/SKIPPED_SCENARIOS):"
if [[ -f "$SCRIPT_DIR/scenarios/SKIPPED_SCENARIOS" ]]; then
  awk '!/^#/ && !/^$/ {
    split($0, parts, ":")
    scenario = parts[1]
    cod_issues = parts[2]
    printf "  - %s (%s)\n", scenario, cod_issues
  }' "$SCRIPT_DIR/scenarios/SKIPPED_SCENARIOS"
fi

# Step 8: Print results
SKIPPED=$(grep -c '^[a-z]' "$SCRIPT_DIR/scenarios/SKIPPED_SCENARIOS" 2>/dev/null || echo 0)
TOTAL=$((PASSED + FAILED + SKIPPED))
echo ""
echo "=========================================="
echo "Test Results:"
echo "  Enabled:  $PASSED passed, $FAILED failed"
echo "  Skipped:  $SKIPPED scenarios"
echo "  Total:    $TOTAL scenarios"
echo "=========================================="

# Exit with failure if any tests failed
[[ $FAILED -eq 0 ]]
