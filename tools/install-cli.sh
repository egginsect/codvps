#!/usr/bin/env bash
# Install only the built CLI. This never runs the source or the host installer.
set -euo pipefail

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

[[ $# -ge 2 && $# -le 3 ]] || die 'usage: install-cli.sh SOURCE_BINARY PREFIX [DESTDIR]'
source_binary=$1
prefix=$2
destdir=${3:-}

# Keep path interpretation unambiguous, including when DESTDIR is prepended.
validate_path() {
  local label=$1 path=$2
  [[ $path == /* ]] || die "$label must be an absolute path"
  case "$path" in
    *//*|*/./*|*/../*|*/.|*/..|*$'\n'*|*$'\r'*)
      die "$label must not contain repeated separators, . or .. components, or newlines" ;;
  esac
}

validate_path PREFIX "$prefix"
[[ -z $destdir ]] || validate_path DESTDIR "$destdir"
[[ ! -L $source_binary && -f $source_binary && -r $source_binary && -s $source_binary ]] ||
  die "source must be a readable, nonempty regular file (not a symlink): $source_binary"

bindir="${destdir%/}${prefix%/}/bin"
target="$bindir/codvps"

# Inspect every component before entering it; mkdir -p would follow symlinks.
ensure_directory() {
  local path=$1 current='' component
  local -a components
  IFS=/ read -r -a components <<< "${path#/}"
  for component in "${components[@]}"; do
    current="$current/$component"
    [[ ! -L $current ]] || die "refusing symlink in destination path: $current"
    if [[ -e $current ]]; then
      [[ -d $current ]] || die "destination component is not a directory: $current"
    else
      mkdir -m 0755 -- "$current"
    fi
  done
}

check_target() {
  [[ ! -L $target ]] || die "refusing symlink destination: $target"
  [[ ! -e $target || -f $target ]] || die "destination is not a regular file: $target"
}

staged=''
backup_pending=''
cleanup() {
  [[ -z $staged ]] || rm -f -- "$staged"
  [[ -z $backup_pending ]] || rm -f -- "$backup_pending"
  return 0
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

ensure_directory "$bindir"
check_target
staged=$(mktemp "$bindir/.codvps.install.XXXXXXXX")
install -m 0755 -- "$source_binary" "$staged"

# Stage first: a bad source or failed copy cannot damage the installed CLI.
ensure_directory "$bindir"
check_target
backup=''
unchanged=0
if [[ -f $target ]]; then
  if cmp -s -- "$staged" "$target"; then
    # Avoid replacing an identical executable, but repair its mode if needed.
    [[ $(stat -c '%a' -- "$target") != 755 ]] || unchanged=1
  else
    # mktemp reserves a fresh name exclusively; existing backups are never used.
    backup_pending=$(mktemp "$bindir/codvps.backup.XXXXXXXX")
    cp -p -- "$target" "$backup_pending"
    backup=$backup_pending
    backup_pending=''
  fi
fi

if ((unchanged)); then
  printf 'codvps is already up to date at %s\n' "$target"
else
  # Both paths are in bin, so replacement is an atomic same-filesystem rename.
  mv -fT -- "$staged" "$target"
  staged=''
  printf 'Installed codvps at %s\n' "$target"
fi
[[ -z $backup ]] || printf 'Previous binary saved at %s\n' "$backup"

if [[ -n $destdir ]]; then
  printf 'Staged for installation at %s/bin/codvps\n' "${prefix%/}"
else
  case ":${PATH:-}:" in
    *":$bindir:"*) ;;
    *) printf "To use it in this shell: export PATH=%q:\"\$PATH\"\n" "$bindir" ;;
  esac
  printf 'Check the CLI with: %q version\n' "$target"
fi
