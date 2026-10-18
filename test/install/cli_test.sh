#!/usr/bin/env bash
# Hermetic tests: fixture sources must never execute; privileged/network commands
# are rejected, and every installation stays beneath the temporary test root.
set -euo pipefail

REPO=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
SCRIPT="$REPO/tools/install-cli.sh"
WORK=$(mktemp -d)
trap 'rm -rf -- "$WORK"' EXIT
STUBS="$WORK/stubs"
LOG="$WORK/forbidden.log"
FAILED=0
CURRENT=''
mkdir -p "$STUBS" "$WORK/home" "$WORK/tmp"
: >"$LOG"

fail() {
  printf 'FAIL [%s]: %s\n' "$CURRENT" "$*" >&2
  FAILED=1
}

for cmd in sudo curl wget apt apt-get dpkg systemctl loginctl ufw swapon \
  mkswap fallocate apparmor_parser useradd runuser ssh ssh-keygen gh claude \
  codex opencode cursor npm node codvps; do
  cat >"$STUBS/$cmd" <<EOF
#!/bin/sh
printf '%s %s\n' "$cmd" "\$*" >>"$LOG"
exit 97
EOF
done

# Inject ordinary copy/rename failures to exercise the pre-commit boundaries.
for cmd in install cp mv; do
  real=$(command -v "$cmd")
  cat >"$STUBS/$cmd" <<EOF
#!/usr/bin/env bash
if [[ \${FAIL_COMMAND:-} == $cmd ]]; then
  if [[ $cmd != mv ]]; then printf 'partial copy\n' >"\${!#}"; fi
  printf 'injected $cmd failure\n' >&2
  exit 98
fi
exec "$real" "\$@"
EOF
done
chmod 0755 "$STUBS"/*

fixture() {
  cat >"$1" <<EOF
#!/bin/sh
# fixture version $2
printf 'source was executed\n' >>"$LOG"
exit 97
EOF
  chmod 0644 "$1"
}
fixture "$WORK/v1" 1
fixture "$WORK/v2" 2
fixture "$WORK/v3" 3

run_install() {
  set +e
  OUT=$(env -i "HOME=$WORK/home" "PATH=$STUBS:/usr/bin:/bin" \
    "TMPDIR=$WORK/tmp" "FAIL_COMMAND=${FAIL_COMMAND:-}" \
    bash "$SCRIPT" "$@" 2>&1)
  RC=$?
  set -e
}

assert_ok() { [[ $RC -eq 0 ]] || fail "rc=$RC: $OUT"; }
assert_refused() { [[ $RC -ne 0 ]] || fail "unexpected success: $OUT"; }
assert_contents() { cmp -s -- "$1" "$2" || fail "contents differ: $1 and $2"; }
assert_mode() { [[ $(stat -c '%a' -- "$1") == 755 ]] || fail "not mode 0755: $1"; }
assert_clean() {
  [[ -z $(find "$WORK" -name '.codvps.install.*' -print) ]] || fail 'staging files were left behind'
}
backup_count() { find "$1" -maxdepth 1 -name 'codvps.backup.*' -type f | wc -l; }

CURRENT='first installation and PATH guidance'
PREFIX="$WORK/home/.local"
TARGET="$PREFIX/bin/codvps"
run_install "$WORK/v1" "$PREFIX"
assert_ok
assert_contents "$WORK/v1" "$TARGET"
assert_mode "$TARGET"
[[ $OUT == *'export PATH='* && $OUT == *' version'* ]] || fail "missing PATH/CLI guidance: $OUT"
[[ $(backup_count "$PREFIX/bin") -eq 0 ]] || fail 'first install created a backup'
assert_clean

CURRENT='identical installation is a no-op'
inode=$(stat -c '%i' -- "$TARGET")
run_install "$WORK/v1" "$PREFIX"
assert_ok
[[ $(stat -c '%i' -- "$TARGET") == "$inode" ]] || fail 'identical binary was replaced'
[[ $(backup_count "$PREFIX/bin") -eq 0 ]] || fail 'identical binary was backed up'
[[ $OUT == *'already up to date'* ]] || fail "missing no-op notice: $OUT"
assert_clean

CURRENT='identical contents with wrong mode'
chmod 0600 "$TARGET"
run_install "$WORK/v1" "$PREFIX"
assert_ok
assert_contents "$WORK/v1" "$TARGET"
assert_mode "$TARGET"
[[ $(backup_count "$PREFIX/bin") -eq 0 ]] || fail 'mode repair created a redundant backup'

CURRENT='upgrades preserve old binaries without clobbering backups'
printf 'existing backup\n' >"$PREFIX/bin/codvps.backup.keep"
run_install "$WORK/v2" "$PREFIX"
assert_ok
assert_contents "$WORK/v2" "$TARGET"
assert_mode "$TARGET"
mapfile -t backups < <(find "$PREFIX/bin" -maxdepth 1 -name 'codvps.backup.*' ! -name '*.keep')
[[ ${#backups[@]} -eq 1 ]] || fail 'upgrade did not create one backup'
assert_contents "$WORK/v1" "${backups[0]}"
run_install "$WORK/v3" "$PREFIX"
assert_ok
assert_contents "$WORK/v3" "$TARGET"
[[ $(backup_count "$PREFIX/bin") -eq 3 ]] || fail 'second upgrade reused a backup'
[[ $(cat "$PREFIX/bin/codvps.backup.keep") == 'existing backup' ]] || fail 'existing backup overwritten'
assert_contents "$WORK/v1" "${backups[0]}"
found_v2=0
for backup in "$PREFIX/bin"/codvps.backup.*; do
  if cmp -s "$WORK/v2" "$backup"; then found_v2=1; fi
done
((found_v2)) || fail 'previous v2 binary not preserved'
assert_clean

CURRENT='spaces in source and prefix'
cp "$WORK/v1" "$WORK/source with spaces"
run_install "$WORK/source with spaces" "$WORK/prefix with spaces/"
assert_ok
assert_contents "$WORK/v1" "$WORK/prefix with spaces/bin/codvps"
assert_mode "$WORK/prefix with spaces/bin/codvps"
[[ $OUT == *'prefix\ with\ spaces/bin'* ]] || fail "PATH guidance did not quote spaces: $OUT"

CURRENT='DESTDIR staging and root prefix'
run_install "$WORK/v1" /usr/local/ "$WORK/stage with spaces/"
assert_ok
assert_contents "$WORK/v1" "$WORK/stage with spaces/usr/local/bin/codvps"
[[ $OUT == *'Staged for installation at /usr/local/bin/codvps'* ]] || fail "missing staging guidance: $OUT"
[[ $OUT != *'export PATH='* ]] || fail 'staging directory was suggested for PATH'
run_install "$WORK/v1" / "$WORK/root-stage"
assert_ok
assert_contents "$WORK/v1" "$WORK/root-stage/bin/codvps"

CURRENT='invalid arguments and paths'
run_install
assert_refused
run_install "$WORK/v1" "$WORK/extra" '' extra
assert_refused
for bad in '' relative "$WORK/../escape" "$WORK/./dot" "$WORK/repeated//separator" \
  "$WORK/end/." "$WORK/end/.." "$WORK/"$'line\nbreak'; do
  run_install "$WORK/v1" "$bad"
  assert_refused
done
for bad in relative "$WORK/../escape" "$WORK/./dot" "$WORK/repeated//separator"; do
  run_install "$WORK/v1" /usr/local "$bad"
  assert_refused
done

CURRENT='symlink ancestry and targets'
mkdir -p "$WORK/outside" "$WORK/symlink-target/bin"
ln -s "$WORK/outside" "$WORK/linked-prefix"
run_install "$WORK/v1" "$WORK/linked-prefix/subdir"
assert_refused
[[ ! -e $WORK/outside/subdir ]] || fail 'symlink ancestor was followed'
ln -s "$WORK/outside" "$WORK/linked-stage"
run_install "$WORK/v1" /usr/local "$WORK/linked-stage"
assert_refused
[[ ! -e $WORK/outside/usr ]] || fail 'symlink DESTDIR was followed'
ln -s "$TARGET" "$WORK/symlink-target/bin/codvps"
run_install "$WORK/v1" "$WORK/symlink-target"
assert_refused
assert_contents "$WORK/v3" "$TARGET"
rm "$WORK/symlink-target/bin/codvps"
ln -s "$WORK/missing" "$WORK/symlink-target/bin/codvps"
run_install "$WORK/v1" "$WORK/symlink-target"
assert_refused
[[ -L $WORK/symlink-target/bin/codvps && ! -e $WORK/missing ]] || fail 'dangling target symlink changed'
ln -s "$WORK/no-directory" "$WORK/dangling-prefix"
run_install "$WORK/v1" "$WORK/dangling-prefix"
assert_refused
assert_clean

CURRENT='non-directory ancestry and non-regular targets'
printf 'file\n' >"$WORK/file-ancestor"
run_install "$WORK/v1" "$WORK/file-ancestor/child"
assert_refused
mkdir -p "$WORK/directory-target/bin/codvps"
run_install "$WORK/v1" "$WORK/directory-target"
assert_refused
[[ -d $WORK/directory-target/bin/codvps ]] || fail 'directory target changed'
mkdir -p "$WORK/fifo-target/bin"
mkfifo "$WORK/fifo-target/bin/codvps"
run_install "$WORK/v1" "$WORK/fifo-target"
assert_refused
[[ -p $WORK/fifo-target/bin/codvps ]] || fail 'FIFO target changed'

CURRENT='bad sources preserve the installed binary'
: >"$WORK/empty"
ln -s "$WORK/v1" "$WORK/linked-source"
for bad in "$WORK/missing-source" "$WORK/empty" "$WORK/outside" "$WORK/linked-source"; do
  run_install "$bad" "$PREFIX"
  assert_refused
  assert_contents "$WORK/v3" "$TARGET"
done
assert_clean

CURRENT='copy and rename failures preserve the installed binary'
for command in install cp mv; do
  before=$(backup_count "$PREFIX/bin")
  inode=$(stat -c '%i' -- "$TARGET")
  FAIL_COMMAND=$command run_install "$WORK/v1" "$PREFIX"
  assert_refused
  assert_contents "$WORK/v3" "$TARGET"
  assert_mode "$TARGET"
  [[ $(stat -c '%i' -- "$TARGET") == "$inode" ]] || fail "$command failure replaced the target"
  if [[ $command != mv ]]; then
    [[ $(backup_count "$PREFIX/bin") -eq $before ]] || fail "$command failure left an incomplete backup"
  fi
  assert_clean
done
[[ -z $(find "$PREFIX/bin" -name 'codvps.backup.*' -empty -print) ]] || fail 'failed backup left an empty file'

CURRENT='no fixture execution, host commands, or shell configuration changes'
[[ ! -s $LOG ]] || fail "unexpected commands: $(cat "$LOG")"
[[ ! -e $WORK/home/.bashrc && ! -e $WORK/home/.profile && ! -e $WORK/home/.zshrc ]] || fail 'shell configuration was modified'

if ((FAILED)); then exit 1; fi
printf 'CLI installer tests passed.\n'
