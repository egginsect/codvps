#!/usr/bin/env bash
# Hermetic source-install fixtures: run bare ./install.sh from a temporary
# checkout, use a fake compiler, and reject network/privileged/provider commands.
# All homes, prefixes, build outputs, and temporary files are private fixtures.
set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
TOOLS="$WORK/tools"
BLOCKED="$WORK/blocked"
SEED="$WORK/source seed"
FAILED=0
CURRENT=''
NUMBER=0
mkdir -p "$TOOLS" "$BLOCKED" "$SEED"

fail() {
  printf 'FAIL [%s]: %s\n' "$CURRENT" "$*" >&2
  FAILED=1
}

# Deliberately omit the host PATH, including any unrelated command called go.
# Only ordinary local file utilities and make are exposed to the installer.
for command in bash sh env dirname basename readlink realpath uname awk sed grep \
  mktemp mkdir chmod install cp mv cmp stat rm cat find wc head sort tr cut date \
  id make sha256sum timeout sleep; do
  real=$(command -v "$command")
  ln -s "$real" "$TOOLS/$command"
done
for command in sudo curl wget apt apt-get dpkg systemctl loginctl ufw swapon \
  mkswap fallocate apparmor_parser useradd runuser ssh ssh-keygen gh claude \
  codex opencode cursor npm node codvps pip pip3 python python3 docker; do
  cat >"$BLOCKED/$command" <<'STUB'
#!/bin/sh
printf '%s %s\n' "${0##*/}" "$*" >>"$FORBIDDEN_LOG"
exit 97
STUB
done
chmod 0755 "$BLOCKED"/*

cp -a "$REPO/install.sh" "$REPO/Makefile" "$REPO/go.mod" "$SEED/"
[[ ! -f $REPO/go.sum ]] || cp -a "$REPO/go.sum" "$SEED/"
cp -a "$REPO/tools" "$REPO/cmd" "$REPO/internal" "$SEED/"
# No installed host toolchain can satisfy this fixture's deliberately distant
# minimum. Absolute system fallback probes can only run `go version`, with
# automatic downloads disabled; they can never become the fixture compiler.
sed -i 's/^go .*/go 99.88.77/' "$SEED/go.mod"

fixture() {
  NUMBER=$((NUMBER + 1))
  CASE="$WORK/case $NUMBER"
  CHECKOUT="$CASE/checkout with spaces"
  HOME_DIR="$CASE/home"
  LOG="$CASE/go.log"
  FORBIDDEN="$CASE/forbidden.log"
  mkdir -p "$CASE" "$HOME_DIR" "$CASE/tmp"
  cp -a "$SEED" "$CHECKOUT"
  : >"$LOG"
  : >"$FORBIDDEN"
  TARGET="$HOME_DIR/.local/bin/codvps"
  TEST_PATH="$BLOCKED:$TOOLS"
  TEST_STDIN=/dev/null
  RUN_DIRECTORY="$CHECKOUT"
  EXTRA_ENV=()
}

# fake_go <path> <identity> <version output> [version exit code]
# The emitted CLI is deliberately non-executable at build time; install-cli
# must set its mode without running it (including any provisioning subcommand).
fake_go() {
  local path=$1 identity=$2 version=$3 version_rc=${4:-0}
  mkdir -p "$(dirname -- "$path")"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'IDENTITY=%q\nVERSION_OUTPUT=%q\nVERSION_RC=%q\n' "$identity" "$version" "$version_rc"
    cat <<'STUB'
set -euo pipefail
printf '%s %s\n' "$IDENTITY" "$*" >>"$GO_LOG"
[[ ${GOTOOLCHAIN:-} == local && ${GOENV:-} == off ]] || {
  printf 'toolchain auto-download or persistent Go settings were not disabled\n' >&2
  exit 96
}
case "${1:-}" in
  version)
    if [[ $IDENTITY == stdin-check ]] && IFS= read -r ignored; then
      printf 'Go probe consumed installer stdin\n' >>"$FORBIDDEN_LOG"
      exit 95
    fi
    if [[ $IDENTITY == hanging ]]; then sleep 30; fi
    printf '%s\n' "$VERSION_OUTPUT"
    exit "$VERSION_RC"
    ;;
  build)
    [[ ${CGO_ENABLED:-} == 0 ]] || exit 96
    [[ -z ${GOFLAGS:-} && ${GOWORK:-} == off ]] || exit 96
    [[ -z ${GOROOT:-} && -z ${GOOS:-} && -z ${GOARCH:-} ]] || {
      printf 'ambient cross-compilation/toolchain environment leaked into build\n' >&2
      exit 96
    }
    shift
    output=''
    package=''
    while (($#)); do
      case "$1" in
        -o) output=$2; shift 2 ;;
        *) package=$1; shift ;;
      esac
    done
    [[ -n $output && $package == ./cmd/codvps && -f cmd/codvps/main.go ]] || exit 96
    if [[ ${FAIL_BUILD:-0} == 1 ]]; then
      printf 'partial build\n' >"$output"
      printf 'injected fixture build failure\n' >&2
      exit 98
    fi
    cat >"$output" <<'BINARY'
#!/bin/sh
# fixture-only source CLI
printf 'built-cli %s\n' "$*" >>"$FORBIDDEN_LOG"
exit 97
BINARY
    chmod 0644 "$output"
    ;;
  *) printf 'unexpected Go operation\n' >&2; exit 96 ;;
esac
STUB
  } >"$path"
  chmod 0755 "$path"
}

good_go() { fake_go "$1" "$2" 'go version go99.88.77 linux/amd64'; }

run_install() {
  set +e
  OUT=$(cd -- "$RUN_DIRECTORY" && env -i \
    "HOME=$HOME_DIR" "PATH=$TEST_PATH" "TMPDIR=$CASE/tmp" \
    "GO_LOG=$LOG" "FORBIDDEN_LOG=$FORBIDDEN" \
    "CODVPS_RELEASES_URL=https://fixture.invalid/releases" \
    "CODVPS_BOOTSTRAP_TTY=$CASE/no-tty" "SUDO_USER=fixture-user" \
    "${EXTRA_ENV[@]}" ./install.sh "$@" <"$TEST_STDIN" 2>&1)
  RC=$?
  set -e
}

assert_ok() { [[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"; }
assert_refused() { [[ $RC -ne 0 ]] || fail "unexpected success: $OUT"; }
assert_source_only() {
  [[ ! -s $FORBIDDEN ]] || fail "network, provisioning, or built CLI ran: $(cat "$FORBIDDEN")"
  [[ ! -e $HOME_DIR/.local/share/codvps ]] || fail 'source mode staged a release'
  [[ ! -e $HOME_DIR/.bashrc && ! -e $HOME_DIR/.profile && ! -e $HOME_DIR/.zshrc ]] || fail 'shell configuration changed'
}
assert_installed() {
  [[ -f $TARGET ]] || { fail "CLI not installed at $TARGET: $OUT"; return; }
  grep -q '^# fixture-only source CLI$' "$TARGET" || fail 'installed unexpected binary'
  [[ $(stat -c '%a' -- "$TARGET") == 755 ]] || fail 'CLI is not mode 0755'
  assert_source_only
}
assert_no_build() { ! grep -q ' build ' "$LOG" || fail "unexpected source build: $(cat "$LOG")"; }
assert_built_with() {
  local count
  grep -q "^$1 build " "$LOG" || fail "selected the wrong Go compiler: $(cat "$LOG")"
  count=$(grep -c ' build ' "$LOG" || true)
  [[ $count == 1 ]] || fail "expected exactly one build, saw $count"
}

CURRENT='bare checkout command builds and installs the CLI'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
run_install
assert_ok
assert_installed
assert_built_with path

CURRENT='skip unrelated PATH go and inspect later PATH entries'
fixture
fake_go "$CASE/unrelated/go" unrelated 'Go: a terminal bookmark manager'
fake_go "$CASE/older/go" older 'go version go99.88.76 linux/amd64'
good_go "$CASE/valid path/go" valid
TEST_PATH="$CASE/unrelated:$CASE/older:$CASE/valid path:$TEST_PATH"
run_install
assert_ok
assert_installed
assert_built_with valid
grep -q '^unrelated version$' "$LOG" || fail 'first PATH candidate was not checked'
grep -q '^older version$' "$LOG" || fail 'older PATH candidate was not checked'

CURRENT='empty and relative PATH entries are skipped safely'
fixture
good_go "$CHECKOUT/go" current-directory
good_go "$CHECKOUT/relative/go" relative-directory
good_go "$CASE/absolute path/go" absolute-directory
TEST_PATH=":relative:$TEST_PATH:$CASE/absolute path:"
run_install
assert_ok
assert_installed
assert_built_with absolute-directory
! grep -q '^current-directory\|^relative-directory' "$LOG" || fail 'relative PATH candidate was executed'

CURRENT='skip failed, prerelease, and malformed version commands'
fixture
fake_go "$CASE/failed/go" failed 'go version go99.88.77 linux/amd64' 1
fake_go "$CASE/prerelease/go" prerelease 'go version go99.88.77rc1 linux/amd64'
fake_go "$CASE/malformed/go" malformed 'go version go99.88.77 linux/amd64 extra-output'
good_go "$CASE/stable/go" stable
TEST_PATH="$CASE/failed:$CASE/prerelease:$CASE/malformed:$CASE/stable:$TEST_PATH"
run_install
assert_ok
assert_installed
assert_built_with stable

CURRENT='Go probes cannot consume installer stdin'
fixture
good_go "$CASE/path/go" stdin-check
TEST_PATH="$CASE/path:$TEST_PATH"
TEST_STDIN="$CASE/input"
printf 'installer input must not reach the Go probe\n' >"$TEST_STDIN"
run_install
assert_ok
assert_installed
assert_built_with stdin-check

CURRENT='a hanging PATH go is bounded and the next compiler is discovered'
fixture
good_go "$CASE/hanging/go" hanging
good_go "$CASE/working/go" working
TEST_PATH="$CASE/hanging:$CASE/working:$TEST_PATH"
run_install
assert_ok
assert_installed
assert_built_with working

CURRENT='a newer Go major version satisfies the minimum'
fixture
fake_go "$CASE/new/go" newer 'go version go100.0.0 linux/amd64'
TEST_PATH="$CASE/new:$TEST_PATH"
run_install
assert_ok
assert_installed
assert_built_with newer

CURRENT='explicit GO path with spaces overrides PATH'
fixture
good_go "$CASE/path/go" path
good_go "$CASE/explicit toolchain/bin/go" explicit
TEST_PATH="$CASE/path:$TEST_PATH"
EXTRA_ENV=("GO=$CASE/explicit toolchain/bin/go")
run_install
assert_ok
assert_installed
assert_built_with explicit
! grep -q '^path ' "$LOG" || fail 'explicit GO fell back to PATH'

CURRENT='literal special characters in GO, PREFIX, and DESTDIR are preserved'
for setting in GO PREFIX DESTDIR; do
  fixture
  # shellcheck disable=SC2016 # These are literal path bytes, not shell/make code.
  literal="$CASE/literal "'dollar$ single'"'"' double" `touch source-backtick-sentinel` $(shell touch source-make-sentinel)'
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  case "$setting" in
    GO)
      good_go "$literal/bin/go" literal
      EXTRA_ENV=("GO=$literal/bin/go")
      ;;
    PREFIX)
      EXTRA_ENV=("PREFIX=$literal")
      TARGET="$literal/bin/codvps"
      ;;
    DESTDIR)
      EXTRA_ENV=(PREFIX=/usr/local "DESTDIR=$literal")
      TARGET="$literal/usr/local/bin/codvps"
      ;;
  esac
  run_install
  assert_ok
  assert_installed
  [[ -z $(find "$CASE" -name source-backtick-sentinel -o -name source-make-sentinel) ]] || fail "$setting path was evaluated as code"
done

CURRENT='explicit GO command name is authoritative over later PATH compilers'
fixture
fake_go "$CASE/first/go" first 'Go: unrelated command'
good_go "$CASE/later/go" later
TEST_PATH="$CASE/first:$CASE/later:$TEST_PATH"
EXTRA_ENV=(GO=go)
run_install
assert_refused
assert_no_build
assert_source_only
! grep -q '^later ' "$LOG" || fail 'explicit GO=go silently selected a later PATH compiler'

CURRENT='invalid explicit GO values are authoritative'
for kind in missing empty unrelated older nonexecutable; do
  fixture
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  override="$CASE/override/go"
  case "$kind" in
    missing) ;;
    empty) override='' ;;
    unrelated) fake_go "$override" override 'Go: not the Go compiler' ;;
    older) fake_go "$override" override 'go version go99.88.76 linux/amd64' ;;
    nonexecutable) good_go "$override" override; chmod 0644 "$override" ;;
  esac
  EXTRA_ENV=("GO=$override")
  run_install
  assert_refused
  assert_no_build
  assert_source_only
  [[ ! -e $TARGET ]] || fail "$kind explicit GO still installed a CLI"
  ! grep -q '^path ' "$LOG" || fail "$kind explicit GO fell back to PATH"
done

CURRENT='a lone broken source marker cannot fall back to a release'
for marker in .git Makefile go.mod cmd/codvps tools/install-source.sh tools/install-cli.sh; do
  fixture
  rm -rf -- "$CHECKOUT"
  mkdir -p "$CHECKOUT" "$(dirname -- "$CHECKOUT/$marker")"
  cp -a "$REPO/install.sh" "$CHECKOUT/install.sh"
  ln -s "$CASE/missing-marker" "$CHECKOUT/$marker"
  run_install
  assert_refused
  assert_no_build
  assert_source_only
done

CURRENT='GOROOT discovers Go without contaminating native build settings'
fixture
good_go "$CASE/goroot with spaces/bin/go" goroot
EXTRA_ENV=("GOROOT=$CASE/goroot with spaces" GOOS=plan9 GOARCH=386 GOTOOLCHAIN=auto GOENV=/nonexistent/fixture-goenv)
run_install
assert_ok
assert_installed
assert_built_with goroot

CURRENT='inherited build flags cannot skip compilation or install a stale CLI'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
printf 'stale build artifact\n' >"$CHECKOUT/codvps"
# shellcheck disable=SC2016 # This make expression must remain inert fixture text.
printf '$(shell touch source-inherited-make-sentinel)\n' >"$CASE/unwanted.mk"
EXTRA_ENV=(MAKEFLAGS=-n GNUMAKEFLAGS=-n MFLAGS=-n GOFLAGS=-n "MAKEFILES=$CASE/unwanted.mk" MAKELEVEL=99)
run_install
assert_ok
assert_installed
assert_built_with path
[[ ! -e $CHECKOUT/source-inherited-make-sentinel ]] || fail 'inherited MAKEFILES was evaluated'

CURRENT='portable adjacent runtime is found without a GO override'
fixture
good_go "$CASE/codvps-cli-runtime/toolchain/go/bin/go" portable
run_install
assert_ok
assert_installed
assert_built_with portable

CURRENT='common user toolchain locations work without modifying PATH'
for location in .local/go go .go .local/share/go sdk/go sdk/go99.88.77; do
  fixture
  good_go "$HOME_DIR/$location/bin/go" user-root
  run_install
  assert_ok
  assert_installed
  assert_built_with user-root
done

CURRENT='no usable Go fails without downloads or provisioning'
fixture
fake_go "$CASE/unrelated/go" unrelated 'Go: not the compiler'
fake_go "$CASE/old/go" old 'go version go99.88.76 linux/amd64'
TEST_PATH="$CASE/unrelated:$CASE/old:$TEST_PATH"
run_install
assert_refused
assert_no_build
assert_source_only
[[ ! -e $TARGET ]] || fail 'missing Go still installed a CLI'
[[ $OUT == *Go* || $OUT == *go* ]] || fail "missing actionable Go diagnostic: $OUT"

CURRENT='PREFIX and DESTDIR stage the CLI without writing the real prefix'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
EXTRA_ENV=(PREFIX=/usr/local "DESTDIR=$CASE/stage with spaces")
TARGET="$CASE/stage with spaces/usr/local/bin/codvps"
run_install
assert_ok
assert_installed
[[ ! -e $HOME_DIR/.local/bin/codvps ]] || fail 'DESTDIR ignored'
[[ $OUT == *'Staged for installation at /usr/local/bin/codvps'* ]] || fail "missing staging guidance: $OUT"

CURRENT='source build failure preserves the installed CLI'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
mkdir -p "$(dirname -- "$TARGET")"
printf 'existing CLI\n' >"$TARGET"
EXTRA_ENV=(FAIL_BUILD=1)
run_install
assert_refused
assert_source_only
[[ $(cat "$TARGET") == 'existing CLI' ]] || fail 'build failure replaced installed CLI'
[[ $OUT == *'injected fixture build failure'* ]] || fail "build failure was hidden: $OUT"

CURRENT='missing source files fail closed, never becoming a release install'
for missing in tools/install-source.sh tools/install-cli.sh Makefile go.mod cmd/codvps; do
  fixture
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  rm -rf -- "${CHECKOUT:?}/${missing:?}"
  run_install
  assert_refused
  assert_no_build
  assert_source_only
  [[ ! -e $TARGET ]] || fail "missing $missing still installed a CLI"
done

CURRENT='invalid go.mod contents fail closed without build or download'
for malformed in wrong-module missing-version duplicate-version prerelease; do
  fixture
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  case "$malformed" in
    wrong-module) sed -i 's|^module .*|module example.invalid/other|' "$CHECKOUT/go.mod" ;;
    missing-version) sed -i '/^go /d' "$CHECKOUT/go.mod" ;;
    duplicate-version) printf '\ngo 99.88.77\n' >>"$CHECKOUT/go.mod" ;;
    prerelease) sed -i 's/^go .*/go 99.88.77rc1/' "$CHECKOUT/go.mod" ;;
  esac
  run_install
  assert_refused
  assert_no_build
  assert_source_only
done

CURRENT='unknown source arguments are refused before any build or release'
for argument in --unknown install --prefix=/tmp/forbidden; do
  fixture
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  run_install "$argument"
  assert_refused
  assert_no_build
  assert_source_only
done

CURRENT='symlinked installer stays source-only or refuses safely'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
mkdir -p "$CASE/launcher"
ln -s "$CHECKOUT/install.sh" "$CASE/launcher/install.sh"
RUN_DIRECTORY="$CASE/launcher"
run_install
assert_source_only
if [[ $RC -eq 0 ]]; then assert_installed; else assert_no_build; fi

CURRENT='source destination symlink is rejected by the safe CLI installer'
fixture
good_go "$CASE/path/go" path
TEST_PATH="$CASE/path:$TEST_PATH"
mkdir -p "$CASE/outside"
ln -s "$CASE/outside" "$HOME_DIR/.local"
run_install
assert_refused
assert_source_only
[[ ! -e $CASE/outside/bin ]] || fail 'source installation followed a destination symlink'

CURRENT='explicit VPS and legacy release flags still select bootstrap'
# curl is a refusal stub here. Reaching it, without a source build, proves
# dispatch; bootstrap_test.sh separately verifies full downloaded/piped flows.
for flag in --vps --version --components --switch --yes -y; do
  fixture
  good_go "$CASE/path/go" path
  TEST_PATH="$CASE/path:$TEST_PATH"
  arguments=("$flag")
  case "$flag" in
    --version) arguments+=(v0.1.0) ;;
    --components) arguments+=(none) ;;
    --switch) arguments+=(none) ;;
  esac
  run_install "${arguments[@]}"
  assert_refused
  assert_no_build
  grep -q '^curl ' "$FORBIDDEN" || fail "$flag did not select release bootstrap: $OUT"
  ! grep -qv '^curl ' "$FORBIDDEN" || fail "unexpected legacy command: $(cat "$FORBIDDEN")"
  [[ ! -e $TARGET ]] || fail 'legacy flag performed a source install'
done

if ((FAILED)); then exit 1; fi
printf 'Source installer tests passed.\n'
