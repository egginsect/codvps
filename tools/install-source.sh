#!/usr/bin/env bash
# Build and install the source CLI only. Never download a compiler or run codvps.
set -euo pipefail

die() {
  printf 'install.sh: error: %s\n' "$*" >&2
  exit 1
}

# Compare stable Go versions without executing any Go toolchain selection.
# Keep numeric fields bounded before shell arithmetic, including stub output.
version_at_least() {
  local actual=$1 minimum=$2 i av mv
  local -a a m
  IFS=. read -r -a a <<< "$actual"
  IFS=. read -r -a m <<< "$minimum"
  for i in 0 1 2; do
    av=${a[$i]:-0}
    mv=${m[$i]:-0}
    [[ ${#av} -le 4 && ${#mv} -le 4 ]] || return 1
    ((10#$av >= 10#$mv)) || return 1
    ((10#$av == 10#$mv)) || return 0
  done
}

# A filename is not evidence of a Go compiler (e.g. /usr/bin/go may be a game).
# Clear GOROOT so that each executable locates its own matching standard library.
probe_go() {
  local candidate=$1 minimum=$2 output version
  [[ -f $candidate && -x $candidate ]] || return 1
  output=$(GOROOT='' GOENV=off GOTOOLCHAIN=local timeout --kill-after=1s 5s "$candidate" version </dev/null 2>/dev/null) || return 1
  [[ $output =~ ^go[[:space:]]version[[:space:]]go([0-9]+\.[0-9]+(\.[0-9]+)?)[[:space:]][^[:space:]]+/[^[:space:]]+$ ]] || return 1
  version=${BASH_REMATCH[1]}
  version_at_least "$version" "$minimum" || return 1
  printf '%s\n' "$candidate"
}

# Search absolute PATH directories only; empty/relative entries could select
# an unrelated executable from whichever directory invoked this installer.
path_candidates() {
  local directory
  local -a entries
  IFS=: read -r -a entries <<< "${PATH:-}"
  for directory in "${entries[@]}"; do
    [[ $directory == /* ]] || continue
    printf '%s/go\n' "${directory%/}"
  done
}

find_go() {
  local checkout=$1 minimum=$2 candidate selected root count=0
  if [[ ${GO+x} ]]; then
    [[ -n $GO && $GO != *$'\n'* && $GO != *$'\r'* ]] ||
      die 'GO is set but empty or malformed; set GO to a valid Go compiler'
    candidate=$GO
    if [[ $candidate != /* ]]; then
      if [[ $candidate == */* ]]; then
        candidate="$PWD/$candidate"
      else
        # Explicit GO=go selects the first executable with that name. Do not
        # silently replace an explicitly selected old/invalid compiler.
        local directory
        local -a entries
        IFS=: read -r -a entries <<< "${PATH:-}"
        candidate=''
        for directory in "${entries[@]}"; do
          [[ $directory == /* && -f $directory/$GO && -x $directory/$GO ]] || continue
          candidate="${directory%/}/$GO"
          break
        done
      fi
    fi
    [[ -n $candidate ]] || die "GO=$GO does not name an executable on an absolute PATH entry"
    selected=$(probe_go "$candidate" "$minimum") ||
      die "GO=$GO is not a working stable Go compiler >= $minimum (GOTOOLCHAIN=local); no compiler was downloaded"
    printf '%s\n' "$selected"
    return 0
  fi

  local -a candidates=()
  while IFS= read -r candidate; do candidates+=("$candidate"); done < <(path_candidates)
  [[ -z ${GOROOT:-} ]] || candidates+=("$GOROOT/bin/go")
  if [[ -n ${HOME:-} ]]; then
    candidates+=("$HOME/.local/go/bin/go" "$HOME/go/bin/go" "$HOME/.go/bin/go"
      "$HOME/.local/share/go/bin/go" "$HOME/sdk/go/bin/go")
    # Official versioned SDK installs have this fixed layout. Limit the number
    # examined, and never recurse or scan unrelated directories.
    for root in "$HOME"/sdk/go*; do
      ((count < 64)) || break
      candidates+=("$root/bin/go")
      count=$((count + 1))
    done
  fi
  candidates+=(/usr/local/go/bin/go /opt/go/bin/go /usr/lib/go/bin/go
    /opt/homebrew/opt/go/libexec/bin/go /usr/local/opt/go/libexec/bin/go)
  count=0
  for root in /usr/lib/go-*; do
    ((count < 64)) || break
    candidates+=("$root/bin/go")
    count=$((count + 1))
  done
  # A portable sibling runtime can accompany a checkout without machine paths.
  candidates+=("${checkout%/*}/codvps-cli-runtime/toolchain/go/bin/go")
  for candidate in "${candidates[@]}"; do
    [[ $candidate == /* ]] || continue
    if selected=$(probe_go "$candidate" "$minimum"); then
      printf '%s\n' "$selected"
      return 0
    fi
  done
  die "no working stable Go compiler >= $minimum found; install Go or set GO=/absolute/path/to/go, then rerun ./install.sh (no compiler was downloaded)"
}

main() {
  (($# == 0)) || die 'source installer accepts no arguments; use ./install.sh --help'
  local checkout line module='' minimum='' selected
  checkout=$(cd -P -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P) ||
    die 'cannot locate the source checkout'
  [[ -f $checkout/go.mod && -r $checkout/go.mod ]] || die 'incomplete source checkout: missing readable go.mod'
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^[[:space:]]*module[[:space:]]+([^[:space:]]+) ]]; then
      [[ -z $module ]] || die 'invalid source checkout: duplicate module directive in go.mod'
      module=${BASH_REMATCH[1]}
    elif [[ $line =~ ^[[:space:]]*go[[:space:]]+([0-9]+\.[0-9]+(\.[0-9]+)?)([[:space:]]*//.*)?[[:space:]]*$ ]]; then
      [[ -z $minimum ]] || die 'invalid source checkout: duplicate Go version in go.mod'
      minimum=${BASH_REMATCH[1]}
    fi
  done < "$checkout/go.mod"
  [[ $module == github.com/egginsect/codvps ]] || die 'invalid source checkout: go.mod must declare module github.com/egginsect/codvps'
  [[ -n $minimum ]] || die 'invalid source checkout: go.mod must declare a stable minimum Go version'
  [[ -d $checkout/cmd/codvps && -f $checkout/cmd/codvps/main.go ]] ||
    die 'incomplete source checkout: missing cmd/codvps/main.go'
  [[ -f $checkout/Makefile && -r $checkout/Makefile ]] || die 'incomplete source checkout: missing readable Makefile'
  [[ -f $checkout/tools/install-cli.sh && -x $checkout/tools/install-cli.sh ]] ||
    die 'incomplete source checkout: missing executable tools/install-cli.sh'
  command -v make >/dev/null 2>&1 || die 'make is required to build and install the CLI'
  command -v timeout >/dev/null 2>&1 || die 'timeout (GNU coreutils) is required to safely detect a Go compiler'
  selected=$(find_go "$checkout" "$minimum")

  # Always build for this machine with the selected local compiler. Ignore
  # persisted Go settings, inherited cross-compilation targets and workspaces.
  unset GOROOT GOOS GOARCH GOAMD64 GO386 GOARM GOARM64 GOMIPS GOMIPS64 GOPPC64 GORISCV64 GOFLAGS
  # Inherited make flags (notably -n, -e, and --eval) must not turn an install
  # into a dry run or replace this checkout's build/install recipes.
  unset MAKEFLAGS GNUMAKEFLAGS MFLAGS MAKEOVERRIDES MAKEFILES MAKELEVEL
  export GOTOOLCHAIN=local GOENV=off GOWORK=off
  exec make --no-print-directory -C "$checkout" install "GO=$selected"
}

main "$@"
