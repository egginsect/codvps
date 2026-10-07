#!/usr/bin/env bash
# codvps bootstrap installer.
#
#   curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh | bash
#   curl -fsSL .../install.sh | bash -s -- --version v0.1.0 --components claude,codex --switch none
#
# This script only fetches and verifies the codvps binary; it never touches
# the host itself. It:
#   1. detects the platform (linux amd64 or arm64; anything else is refused);
#   2. resolves the release selector once to an exact tag (--version is
#      exact and never falls back to the latest release);
#   3. downloads that tag's binary and SHA256SUMS over HTTPS and verifies
#      the checksum and the version the binary reports, failing before
#      anything is kept on a mismatch, a missing entry or an unexpected
#      layout;
#   4. keeps the verified binary under ~/.local/share/codvps/<tag>/;
#   5. hands off to `sudo codvps install` (which provisions the host and
#      wires up codvps), forwarding --components and --switch. Under
#      `curl | bash` stdin is this script, so the handoff reads the terminal
#      from /dev/tty; with no terminal it prints the exact next command
#      instead of running it, so a non-interactive run never hangs on a
#      prompt.
#
# Everything is inside functions and `main` runs on the last line, so a
# truncated download defines nothing it could half-execute.

set -euo pipefail

readonly CODVPS_DEFAULT_RELEASES_URL=https://github.com/egginsect/codvps/releases

say() {
  printf '%s\n' "$*" >&2
}

die() {
  printf 'install.sh: error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
usage: install.sh [--version vX.Y.Z] [--components claude,codex|none]
                  [--switch none] [--yes]

Downloads and verifies the codvps release binary for this machine, then
hands off to `sudo codvps install`.

  --version <tag>       exact release to install (default: the latest
                        release, resolved once to its tag)
  --components <list>   forwarded to `codvps install`
  --switch <name>       forwarded to `codvps install`
  --yes                 do not ask for confirmation before the handoff
  -h, --help            show this help

Environment:
  CODVPS_VERSION        same as --version
  CODVPS_RELEASES_URL   HTTPS base of the releases
                        (default: https://github.com/egginsect/codvps/releases)
EOF
}

# detect_arch prints the release architecture for this machine.
detect_arch() {
  local os machine
  os=$(uname -s)
  machine=$(uname -m)
  [[ $os == Linux ]] || die "unsupported operating system: $os (codvps runs on Linux)"
  case "$machine" in
    x86_64 | amd64) printf 'amd64\n' ;;
    aarch64 | arm64) printf 'arm64\n' ;;
    *) die "unsupported architecture: $machine (codvps releases exist for linux amd64 and arm64)" ;;
  esac
}

# fetch <url> <dest>: HTTPS only, failing on any HTTP error.
fetch() {
  curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1"
}

# normalize_tag accepts vX.Y.Z (or X.Y.Z) with an optional pre-release or
# build suffix and prints the tag form.
normalize_tag() {
  local tag=$1
  [[ $tag == v* ]] || tag="v$tag"
  [[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] ||
    die "not a release version: $1 (expected vX.Y.Z)"
  printf '%s\n' "$tag"
}

# resolve_tag turns the release selector into one exact tag. "latest" asks
# the releases page where /latest redirects; an explicit version is used
# as given, and a missing release fails at download instead of falling
# back to another one.
resolve_tag() {
  local selector=$1 releases=$2 effective
  if [[ $selector != latest ]]; then
    normalize_tag "$selector"
    return
  fi
  effective=$(curl -fsSL --proto '=https' --tlsv1.2 -o /dev/null -w '%{url_effective}' "$releases/latest") ||
    die "could not resolve the latest release from $releases/latest"
  [[ $effective == */tag/* ]] ||
    die "could not resolve the latest release: $releases/latest did not redirect to a release tag"
  normalize_tag "${effective##*/tag/}"
}

# verify_checksum <sums> <asset> <file>: SHA256SUMS must name the asset
# exactly once, with a well-formed digest that matches the file.
verify_checksum() {
  local sums=$1 asset=$2 file=$3 lines want got
  lines=$(awk -v a="$asset" '$2 == a || $2 == "*" a' "$sums")
  [[ -n $lines ]] || die "SHA256SUMS has no entry for $asset"
  [[ $lines != *$'\n'* ]] || die "SHA256SUMS lists $asset more than once"
  want=${lines%% *}
  [[ $want =~ ^[0-9a-f]{64}$ ]] || die "SHA256SUMS has a malformed digest for $asset"
  got=$(sha256sum "$file")
  got=${got%% *}
  [[ $got == "$want" ]] ||
    die "checksum mismatch for $asset: expected $want, got $got; nothing was installed"
  printf '%s\n' "$got"
}

# verify_version <binary> <tag>: the binary must report the tag it was
# downloaded as.
verify_version() {
  local binary=$1 tag=$2 out
  chmod 0755 "$binary"
  out=$("$binary" version 2>/dev/null) || die "the downloaded binary does not run on this machine"
  [[ $out == "codvps version $tag "* ]] ||
    die "the downloaded binary reports '${out%%$'\n'*}', not codvps $tag; nothing was installed"
}

# stage <binary> <tag>: keep the verified binary where the handoff (now or
# later) can run it. The operator owns this copy; `codvps install` copies
# it to /usr/local/bin/codvps as root.
stage() {
  local binary=$1 tag=$2 dir
  dir="${XDG_DATA_HOME:-$HOME/.local/share}/codvps/$tag"
  mkdir -p -- "$dir"
  install -m 0755 -- "$binary" "$dir/codvps.tmp"
  mv -f -- "$dir/codvps.tmp" "$dir/codvps"
  printf '%s\n' "$dir/codvps"
}

# have_tty reports whether the terminal can be opened for the handoff.
have_tty() {
  local tty=$1
  [[ -n $tty ]] && { : <"$tty" && : >>"$tty"; } 2>/dev/null
}

# quote_cmd prints a command line that can be pasted back into a shell.
quote_cmd() {
  local out='' arg
  for arg in "$@"; do
    if [[ $arg =~ ^[A-Za-z0-9_./=,:@+-]+$ ]]; then
      out+="$arg "
    else
      out+="$(printf '%q' "$arg") "
    fi
  done
  printf '%s\n' "${out% }"
}

main() {
  local version=${CODVPS_VERSION:-latest}
  local releases=${CODVPS_RELEASES_URL:-$CODVPS_DEFAULT_RELEASES_URL}
  local tty=${CODVPS_BOOTSTRAP_TTY:-/dev/tty}
  local assume_yes=0
  local -a forward=()

  while (($#)); do
    case "$1" in
      --version)
        (($# >= 2)) || die '--version requires a value, e.g. --version v0.1.0'
        version=$2
        shift 2
        ;;
      --version=*) version=${1#*=}; shift ;;
      --components | --switch)
        (($# >= 2)) || die "$1 requires a value"
        forward+=("$1" "$2")
        shift 2
        ;;
      --components=* | --switch=*) forward+=("$1"); shift ;;
      --yes | -y) assume_yes=1; shift ;;
      -h | --help) usage; return 0 ;;
      *) die "unknown argument: $1 (see install.sh --help)" ;;
    esac
  done

  [[ $releases == https://* ]] || die "CODVPS_RELEASES_URL must be an https:// URL: $releases"
  releases=${releases%/}

  local sudo_cmd=(sudo)
  if ((EUID == 0)); then
    [[ -n ${SUDO_USER:-} && $SUDO_USER != root ]] ||
      die 'run the installer as a normal sudo-capable user (not as root), because systemd --user + linger need a login account'
    sudo_cmd=()
  fi
  local tool
  for tool in curl sha256sum awk install; do
    command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
  done

  local arch tag asset
  arch=$(detect_arch)
  tag=$(resolve_tag "$version" "$releases")
  asset="codvps-linux-$arch"
  say "codvps $tag for linux/$arch"

  local tmp
  tmp=$(mktemp -d)
  # shellcheck disable=SC2064 # expand now: $tmp is local to main
  trap "rm -rf -- '$tmp'" EXIT

  fetch "$releases/download/$tag/SHA256SUMS" "$tmp/SHA256SUMS" ||
    die "could not download SHA256SUMS for $tag (does release $tag exist?)"
  fetch "$releases/download/$tag/$asset" "$tmp/$asset" ||
    die "could not download $asset for $tag"
  local digest
  digest=$(verify_checksum "$tmp/SHA256SUMS" "$asset" "$tmp/$asset")
  verify_version "$tmp/$asset" "$tag"
  say "verified $asset (sha256 $digest)"

  local staged
  staged=$(stage "$tmp/$asset" "$tag")
  local -a handoff=("${sudo_cmd[@]}" "$staged" install "${forward[@]}")

  cat >&2 <<EOF

Verified binary kept at $staged.
Next, as root, \`codvps install\` will:
  - install /usr/local/bin/codvps and the codvps systemd units;
  - provision the host for the selected components: apt packages, the GitHub
    CLI and NodeSource apt repositories, the Claude, Codex and uv installers,
    the bubblewrap AppArmor profile, swap and ufw (each step is printed
    before it runs; --skip-provision skips them);
  - enable linger for ${SUDO_USER:-$(id -un)} and write the operator's codvps state.
EOF

  if ! have_tty "$tty"; then
    say ''
    say 'No terminal is available, so nothing more was run. To finish, run:'
    printf '  %s\n' "$(quote_cmd "${handoff[@]}")"
    return 0
  fi

  if ((!assume_yes)); then
    local reply=''
    printf '\nRun %s now? [y/N] ' "$(quote_cmd "${handoff[@]}")" >>"$tty"
    IFS= read -r reply <"$tty" || true
    if [[ $reply != [yY] && $reply != [yY][eE][sS] ]]; then
      say 'Not run. To finish later, run:'
      printf '  %s\n' "$(quote_cmd "${handoff[@]}")"
      return 0
    fi
  fi

  say "+ $(quote_cmd "${handoff[@]}")"
  "${handoff[@]}" <"$tty"

  say ''
  say 'Next: configure SSH and Git for GitHub with'
  say '  codvps login github'
}

main "$@"
