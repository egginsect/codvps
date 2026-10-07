#!/usr/bin/env bash
# Tests for the bootstrap installer, install.sh.
#
# Hermetic: every privileged or network command is a stub first on PATH
# that logs its argv and exits 97, except curl (which serves a local
# release fixture for https://fixture.invalid only), uname (which reports
# the platform under test) and, in the handoff cases only, sudo (which
# runs the staged fixture binary and nothing else). HOME and TMPDIR are
# temporary. Nothing reaches the network or the host.
set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
SCRIPT="$REPO/install.sh"
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
STUBS="$WORK/stubs"
WEB="$WORK/web"
LOG="$WORK/calls.log"
BASE=https://fixture.invalid/releases
FAILED=0
CURRENT=''

fail() {
  printf 'FAIL [%s]: %s\n' "$CURRENT" "$*"
  FAILED=1
}

# ---------------------------------------------------------------- stubs --
mkdir -p "$STUBS"
for cmd in sudo apt-get apt dpkg wget systemctl loginctl ufw swapon mkswap \
  fallocate apparmor_parser useradd claude codex gh ssh ssh-keygen npm node \
  codvps runuser; do
  cat >"$STUBS/$cmd" <<EOF
#!/bin/sh
printf '%s %s\n' "$cmd" "\$*" >>"$LOG"
exit 97
EOF
done

# sudo runs only the staged fixture binary, and only when a case allows it.
cat >"$STUBS/sudo" <<EOF
#!/bin/sh
printf 'sudo %s\n' "\$*" >>"$LOG"
if [ "\${FAKE_SUDO:-}" = 1 ]; then
  case "\$1" in
    "\$HOME"/.local/share/codvps/*/codvps) exec "\$@" ;;
  esac
fi
exit 97
EOF

cat >"$STUBS/uname" <<'EOF'
#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;;
  -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) exit 97 ;;
esac
EOF

# curl serves $WEB for https://fixture.invalid/releases/..., answers the
# /latest redirect from $WEB/latest-tag, and refuses everything else.
cat >"$STUBS/curl" <<EOF
#!/usr/bin/env bash
printf 'curl %s\n' "\$*" >>"$LOG"
out='' write='' url=''
while ((\$#)); do
  case "\$1" in
    -o) out=\$2; shift 2 ;;
    -w) write=\$2; shift 2 ;;
    --proto) [[ \$2 == '=https' ]] || exit 97; shift 2 ;;
    -*) shift ;;
    *) url=\$1; shift ;;
  esac
done
case "\$url" in
  $BASE/*) rel=\${url#$BASE/} ;;
  *) exit 97 ;;
esac
if [[ \$rel == latest ]]; then
  [[ -f "$WEB/latest-tag" ]] || exit 22
  [[ \$write == '%{url_effective}' ]] && printf '%s/tag/%s' "$BASE" "\$(cat "$WEB/latest-tag")"
  exit 0
fi
[[ -f "$WEB/\$rel" ]] || exit 22
cp -- "$WEB/\$rel" "\$out"
EOF
chmod 0755 "$STUBS"/*

# -------------------------------------------------------------- fixture --
# release <tag> [reported-version]: publish fixture binaries for both
# architectures and their SHA256SUMS.
release() {
  local tag=$1 reports=${2:-$1} dir="$WEB/download/$1" arch
  mkdir -p "$dir"
  for arch in amd64 arm64; do
    cat >"$dir/codvps-linux-$arch" <<EOF
#!/bin/sh
# fixture codvps $tag linux/$arch
case "\$1" in
  version) echo "codvps version $reports (commit fixture, built fixture)" ;;
  install) printf 'fixture-codvps %s\n' "\$*" >>"$LOG" ;;
  *) exit 97 ;;
esac
EOF
  done
  (cd "$dir" && sha256sum codvps-linux-amd64 codvps-linux-arm64 >SHA256SUMS)
}

release v0.1.0
release v0.2.0 v0.1.0 # a release whose binary reports another version
printf 'v0.1.0' >"$WEB/latest-tag"

# ---------------------------------------------------------------- cases --
STAGE_ROOT=''

# run_install <how> [args...]: run install.sh with a fresh log; <how> is
# "pipe" (curl | bash: the script on stdin) or "file" (inspect first:
# bash ./install.sh). Sets OUT and RC.
run_install() {
  local how=$1
  shift
  : >"$LOG"
  rm -rf -- "$WORK/tmp"
  [[ ${KEEP_HOME:-0} == 1 ]] || rm -rf -- "${WORK:?}/home"
  mkdir -p "$WORK/tmp" "$WORK/home"
  STAGE_ROOT="$WORK/home/.local/share/codvps"
  local -a envv=(env -i "HOME=$WORK/home" "PATH=$STUBS:/usr/bin:/bin" "TMPDIR=$WORK/tmp"
    "CODVPS_RELEASES_URL=${RELEASES_URL:-$BASE}" "CODVPS_BOOTSTRAP_TTY=${TTY_PATH:-$WORK/no-tty}"
    "FAKE_UNAME_S=${FAKE_UNAME_S:-Linux}" "FAKE_UNAME_M=${FAKE_UNAME_M:-x86_64}" "FAKE_SUDO=${FAKE_SUDO:-0}")
  set +e
  if [[ $how == pipe ]]; then
    OUT=$("${envv[@]}" bash -s -- "$@" <"$SCRIPT" 2>&1)
  else
    cp -- "$SCRIPT" "$WORK/install.sh"
    OUT=$("${envv[@]}" bash "$WORK/install.sh" "$@" 2>&1 </dev/null)
  fi
  RC=$?
  set -e
}

# only_called <cmd...>: the log names no command outside the list.
only_called() {
  local line cmd allowed ok
  while IFS= read -r line; do
    cmd=${line%% *}
    ok=0
    for allowed in "$@"; do [[ $cmd == "$allowed" ]] && ok=1; done
    ((ok)) || fail "unexpected command: $line"
  done <"$LOG"
}

# nothing_installed: a refused run keeps no binary and no temp files.
nothing_installed() {
  [[ ! -e $STAGE_ROOT ]] || fail "a refused run kept $STAGE_ROOT"
  [[ -z "$(ls -A "$WORK/tmp")" ]] || fail "a refused run left temp files: $(ls -A "$WORK/tmp")"
  ! grep -q '^sudo ' "$LOG" || fail "a refused run called sudo"
}

CURRENT='latest, non-TTY, curl | bash'
run_install pipe --components claude,codex --switch none
[[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"
staged="$STAGE_ROOT/v0.1.0/codvps"
grep -q 'fixture codvps v0.1.0 linux/amd64' "$staged" || fail "did not keep the amd64 v0.1.0 binary"
[[ $OUT == *"No terminal is available, so nothing more was run. To finish, run:"* ]] || fail "no non-TTY notice: $OUT"
[[ $OUT == *"  sudo $staged install --components claude,codex --switch none"* ]] || fail "exact next command not printed: $OUT"
grep -q "^curl .*$BASE/latest\$" "$LOG" || fail "latest was not resolved"
grep -q "^curl .*$BASE/download/v0.1.0/SHA256SUMS\$" "$LOG" || fail "SHA256SUMS not fetched for the resolved tag"
only_called curl
[[ -z "$(ls -A "$WORK/tmp")" ]] || fail "temp files left behind"

CURRENT='idempotent rerun'
first=$(sha256sum "$staged")
KEEP_HOME=1 run_install pipe --components claude,codex --switch none
[[ $RC -eq 0 ]] || fail "rerun rc=$RC: $OUT"
[[ "$(sha256sum "$staged")" == "$first" ]] || fail "rerun changed the kept binary"
[[ "$(find "$STAGE_ROOT" -type f | wc -l)" -eq 1 ]] || fail "rerun left extra files: $(find "$STAGE_ROOT" -type f)"

CURRENT='arm64 selection'
FAKE_UNAME_M=aarch64 run_install pipe
[[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"
grep -q 'fixture codvps v0.1.0 linux/arm64' "$STAGE_ROOT/v0.1.0/codvps" || fail "did not select the arm64 binary"
grep -q "/download/v0.1.0/codvps-linux-arm64\$" "$LOG" || fail "arm64 asset not fetched"
! grep -q 'codvps-linux-amd64' "$LOG" || fail "fetched the amd64 asset on arm64"

CURRENT='unsupported architecture'
FAKE_UNAME_M=riscv64 run_install pipe
[[ $RC -ne 0 && $OUT == *'unsupported architecture: riscv64'* ]] || fail "rc=$RC: $OUT"
[[ ! -s $LOG ]] || fail "downloaded on an unsupported architecture: $(cat "$LOG")"
nothing_installed

CURRENT='unsupported OS'
FAKE_UNAME_S=Darwin run_install pipe
[[ $RC -ne 0 && $OUT == *'unsupported operating system: Darwin'* ]] || fail "rc=$RC: $OUT"
nothing_installed

CURRENT='checksum mismatch'
cp "$WEB/download/v0.1.0/SHA256SUMS" "$WORK/sums.good"
sed -i '/codvps-linux-amd64/s/^./0/; /codvps-linux-amd64/s/^0\(.\)/1\1/' "$WEB/download/v0.1.0/SHA256SUMS"
FAKE_SUDO=1 TTY_PATH="$WORK/tty-yes" run_install pipe --components claude --yes
[[ $RC -ne 0 && $OUT == *'checksum mismatch for codvps-linux-amd64'* ]] || fail "rc=$RC: $OUT"
nothing_installed
! grep -q '^fixture-codvps' "$LOG" || fail "install ran after a checksum mismatch"

CURRENT='missing checksum entry'
grep -v codvps-linux-amd64 "$WORK/sums.good" >"$WEB/download/v0.1.0/SHA256SUMS"
run_install pipe
[[ $RC -ne 0 && $OUT == *'SHA256SUMS has no entry for codvps-linux-amd64'* ]] || fail "rc=$RC: $OUT"
nothing_installed

CURRENT='duplicate checksum entry'
cat "$WORK/sums.good" "$WORK/sums.good" >"$WEB/download/v0.1.0/SHA256SUMS"
run_install pipe
[[ $RC -ne 0 && $OUT == *'lists codvps-linux-amd64 more than once'* ]] || fail "rc=$RC: $OUT"
nothing_installed
cp "$WORK/sums.good" "$WEB/download/v0.1.0/SHA256SUMS"

CURRENT='wrong --version (no such release, no fallback)'
run_install pipe --version v9.9.9
[[ $RC -ne 0 && $OUT == *'could not download SHA256SUMS for v9.9.9'* ]] || fail "rc=$RC: $OUT"
! grep -q '/latest' "$LOG" || fail "an explicit version fell back to latest"
nothing_installed

CURRENT='wrong --version (binary reports another version)'
run_install pipe --version v0.2.0
[[ $RC -ne 0 && $OUT == *"reports 'codvps version v0.1.0"*'not codvps v0.2.0'* ]] || fail "rc=$RC: $OUT"
nothing_installed

CURRENT='malformed --version'
run_install pipe --version latest-ish
[[ $RC -ne 0 && $OUT == *'not a release version: latest-ish'* ]] || fail "rc=$RC: $OUT"
nothing_installed

CURRENT='non-https releases URL'
RELEASES_URL=http://fixture.invalid/releases run_install pipe
[[ $RC -ne 0 && $OUT == *'must be an https:// URL'* ]] || fail "rc=$RC: $OUT"
nothing_installed

CURRENT='TTY handoff forwards components and switch'
printf 'y\n' >"$WORK/tty-yes"
FAKE_SUDO=1 TTY_PATH="$WORK/tty-yes" run_install pipe --components codex --switch=none
[[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"
grep -Fxq "sudo $STAGE_ROOT/v0.1.0/codvps install --components codex --switch=none" "$LOG" || fail "handoff not run through sudo: $(cat "$LOG")"
grep -Fxq 'fixture-codvps install --components codex --switch=none' "$LOG" || fail "components/switch not forwarded: $(cat "$LOG")"
[[ $OUT == *'codvps login github'* ]] || fail "login github not suggested: $OUT"
plan=${OUT%%Run *}
[[ $plan == *'provision the host for the selected components'* ]] || fail "the plan was not shown before the prompt: $OUT"
only_called curl sudo fixture-codvps

CURRENT='TTY handoff declined'
printf 'n\n' >"$WORK/tty-no"
FAKE_SUDO=1 TTY_PATH="$WORK/tty-no" run_install pipe --components claude
[[ $RC -eq 0 && $OUT == *'Not run. To finish later, run:'* ]] || fail "rc=$RC: $OUT"
! grep -q '^sudo ' "$LOG" || fail "sudo ran after the operator declined"

CURRENT='TTY handoff with --yes'
: >"$WORK/tty-empty"
FAKE_SUDO=1 TTY_PATH="$WORK/tty-empty" run_install pipe --components claude --yes
[[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"
grep -Fxq 'fixture-codvps install --components claude' "$LOG" || fail "--yes did not hand off: $(cat "$LOG")"

CURRENT='inspect first: download, read, run from a file'
run_install file --help
[[ $RC -eq 0 && $OUT == *'usage: install.sh'* ]] || fail "--help rc=$RC: $OUT"
[[ ! -s $LOG ]] || fail "--help touched the network: $(cat "$LOG")"
run_install file --version 0.1.0 --components claude
[[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"
[[ $OUT == *"  sudo $STAGE_ROOT/v0.1.0/codvps install --components claude"* ]] || fail "exact next command not printed: $OUT"
! grep -q '/latest' "$LOG" || fail "an explicit version resolved latest"

CURRENT='truncated download runs nothing'
size=$(wc -c <"$SCRIPT")
for cut in $((size / 4)) $((size / 2)) $((size - 20)); do
  : >"$LOG"
  set +e
  head -c "$cut" "$SCRIPT" | env -i "HOME=$WORK/home" "PATH=$STUBS:/usr/bin:/bin" \
    "CODVPS_RELEASES_URL=$BASE" "CODVPS_BOOTSTRAP_TTY=$WORK/no-tty" bash -s >/dev/null 2>&1
  set -e
  [[ ! -s $LOG ]] || fail "a script truncated at $cut bytes ran commands: $(cat "$LOG")"
done

if ((FAILED)); then
  printf 'bootstrap_test: FAILED\n'
  exit 1
fi
printf 'bootstrap_test: all cases passed\n'
