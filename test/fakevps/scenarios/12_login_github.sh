#!/usr/bin/env bash
set -euo pipefail

# Test: codvps login github --non-interactive --key generate ...
#
# Covers (see docs/test-parity-login.md for the full mapping):
#   - test_login_github_key_generate_creates_pair   (tests/login-github.sh)
#   - test_login_github_key_mode_0600               (tests/login-github.sh)
#   - test_login_github_register_calls_add_once     (tests/login-github.sh)
#   - test_login_github_register_records_key        (tests/login-github.sh)
#   - test_login_github_known_hosts_adds_entry       (tests/login-github.sh)
#   - test_login_github_known_hosts_calls_api_meta   (tests/login-github.sh)
#   - test_login_github_ssh_config_adds_host_block   (tests/login-github.sh)
#   - test_login_github_ssh_config_adds_identityfile (tests/login-github.sh)
#   - test_login_github_identity_explicit_flags      (tests/login-github.sh)
#   - test_login_github_verify_succeeds              (tests/login-github.sh)
#   - test_zero_heritage_path_login_github_no_clone  (tests/smoke.sh)
#   - test_login_github_key_generate_idempotent       (tests/login-github.sh)
#   - test_login_github_register_no_duplicate_add     (tests/login-github.sh)
#   - test_login_github_known_hosts_idempotent        (tests/login-github.sh)
#   - test_login_github_key_no_choice_pending         (tests/login-github.sh)
#   - test_login_github_ssh_config_skipped_without_key (tests/login-github.sh)
#   - test_login_github_key_generate_requires_passphrase_empty (tests/login-github.sh)
#   - test_login_github_register_pending_without_yes  (tests/login-github.sh)
#   - test_login_github_register_stops_on_list_failure (tests/login-github.sh)
#   - test_login_github_register_requires_scope       (tests/login-github.sh)
#   - test_login_github_known_hosts_stops_without_meta (tests/login-github.sh)
#   - test_login_github_known_hosts_untouched_on_meta_failure (tests/login-github.sh)
#   - test_login_github_known_hosts_hashed_entries_recognized (tests/login-github.sh)
#   - test_login_github_known_hosts_refuses_conflicting_key (tests/login-github.sh)
#   - test_login_github_identity_never_overwritten    (tests/login-github.sh)
#   - test_login_github_identity_pending_without_flags (tests/login-github.sh)
#   - test_login_github_identity_derives_noreply_email (tests/login-github.sh)
#   - test_login_github_verify_fails_on_publickey_error (tests/login-github.sh)
#   - test_login_github_verify_fails_on_account_mismatch (tests/login-github.sh)
#   - test_login_github_verify_pins_selected_key      (tests/login-github.sh)
#   - test_login_github_auth_non_interactive_exit2_logged_out (tests/login-github.sh)
#   - test_login_github_cli_non_interactive_exit2_logged_out (tests/login-github.sh)
#   - test_login_github_cli_never_calls_auth_login    (tests/login-github.sh)
#   - test_login_github_auth_interactive_no_tty_exit2 (tests/login-github.sh;
#     changed it: no terminal now runs the device flow instead of exit 2)
#   - test_login_github_auth_already_logged_in_no_relogin (tests/login-github.sh)
#   - test_git_identity_configured                    (tests/smoke.sh)
#
# The reference drove most of these by calling one step function of its
# CLI script at a time; here every case runs the whole `codvps login
# github` command and asserts the step's summary line, exit code, files
# and fake-tool call log. The first two tests run under the real operator
# $HOME; every later case gets its own scratch HOME, fake state and call
# logs (new_case), so the cases never see each other's keys or config.
#
# Fakes gh/ssh/ssh-keygen/git are re-authored (not copied) from this repo's
# own hermetic fixtures in internal/login/testutil_test.go, adapted to a
# scenario-local bin dir that is placed first on PATH only for this script;
# the real gh/ssh/ssh-keygen/git are never invoked by the command under
# test here. Key/known_hosts/ssh-config state is written under the real
# operator $HOME, matching what a real `codvps login github` run does.

WORK="$HOME/tmp/login12"
BIN="$WORK/bin"
BIN_REALGIT="$WORK/bin-realgit"
STATE_DIR="$WORK/state"
LOG_DIR="$WORK/logs"
KEYPATH="$HOME/.ssh/codvps_github_ed25519"
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

setup() {
  rm -rf "$WORK" "$KEYPATH" "$KEYPATH.pub"
  mkdir -p "$BIN" "$STATE_DIR" "$LOG_DIR" "$HOME/.ssh"
  chmod 0700 "$HOME/.ssh"
  : >"$STATE_DIR/logged-in"

  cat >"$BIN/gh" <<'GHEOF'
#!/usr/bin/env bash
set -u
log_call() { local out="$LOG_DIR/$1.log"; shift; { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"; }
log_call gh "$@"
cmd=${1-}; sub=${2-}
case "$cmd" in
  auth)
    case "$sub" in
      status)
        # gh 2.101 shape: --json hosts only; scopes is one comma-separated
        # string; logged out is {"hosts":{}} at exit 0.
        [[ $* == 'auth status --json hosts' ]] || { echo "unknown JSON field in: $*" >&2; exit 1; }
        scopes='admin:public_key, repo'
        [[ -e "$STATE_DIR/scope-limited" ]] && scopes='repo'
        if [[ -e "$STATE_DIR/logged-in" ]]; then
          printf '{"hosts":{"github.com":[{"state":"success","active":true,"host":"github.com","login":"octocat","tokenSource":"keyring","scopes":"%s","gitProtocol":"ssh"}]}}\n' "$scopes"
        else
          printf '{"hosts":{}}\n'
        fi
        exit 0 ;;
      login)
        # gh's device flow without a terminal: code + URL on stderr, then
        # it waits for the code to be entered elsewhere.
        [[ -t 0 ]] && { echo "fake gh: stdin must not be a terminal for the device flow" >&2; exit 1; }
        echo "! First copy your one-time code: ABCD-1234" >&2
        echo "Open this URL to continue in your web browser: https://github.com/login/device" >&2
        : >"$STATE_DIR/logged-in"; exit 0 ;;
      *) exit 1 ;;
    esac ;;
  api)
    case "$sub" in
      user)
        shift 2; jq_expr=; while (($#)); do case "$1" in --jq) jq_expr=$2; shift 2 ;; *) shift ;; esac; done
        case "$jq_expr" in
          .login) printf 'octocat\n' ;;
          '.name // .login') printf 'octocat\n' ;;
          .id) printf '999\n' ;;
          *) printf '\n' ;;
        esac
        exit 0 ;;
      meta)
        if [[ -e "$STATE_DIR/meta-fails" ]]; then
          printf 'gh: HTTP 503: Service Unavailable (https://api.github.com/meta)\n' >&2
          exit 1
        fi
        shift 2; jq_expr=; while (($#)); do case "$1" in --jq) jq_expr=$2; shift 2 ;; *) shift ;; esac; done
        case "$jq_expr" in
          '.ssh_keys[]') printf 'ssh-ed25519 AAAAFAKEEDKEYDATA\nssh-rsa AAAAFAKERSAKEYDATA\n' ;;
          *) printf '\n' ;;
        esac
        exit 0 ;;
      *) exit 1 ;;
    esac ;;
  ssh-key)
    case "$sub" in
      list)
        if [[ -e "$STATE_DIR/ssh-key-list-fails" ]]; then
          printf 'gh: HTTP 502: Bad Gateway (https://api.github.com/user/keys)\n' >&2
          exit 1
        fi
        cat "$STATE_DIR/ssh-keys.tsv" 2>/dev/null
        exit 0 ;;
      add)
        shift 2; key_path=$1; shift || true
        title=; while (($#)); do case "$1" in --title) title=$2; shift 2 ;; *) shift ;; esac; done
        printf '%s\t%s\t2026-01-01T00:00:00Z\t1\tauthentication\n' "$title" "$(cat "$key_path")" >>"$STATE_DIR/ssh-keys.tsv"
        exit 0 ;;
      *) exit 1 ;;
    esac ;;
  *) exit 1 ;;
esac
GHEOF

  cat >"$BIN/ssh" <<'SSHEOF'
#!/usr/bin/env bash
set -u
log_call() { local out="$LOG_DIR/$1.log"; shift; { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"; }
log_call ssh "$@"
for a in "$@"; do
  if [[ $a == "-G" ]]; then
    cfg="$HOME/.ssh/config"
    if [[ -f $cfg ]]; then
      awk '
        BEGIN { inblock = 0 }
        tolower($1) == "host" {
          inblock = 0
          for (i = 2; i <= NF; i++) if (tolower($i) == "github.com") inblock = 1
          next
        }
        inblock == 1 && tolower($1) == "identityfile" { print "identityfile", $2 }
      ' "$cfg"
    fi
    exit 0
  fi
done
case "$(cat "$STATE_DIR/ssh-mode" 2>/dev/null)" in
  publickey)
    printf 'git@github.com: Permission denied (publickey).\n' >&2
    exit 255 ;;
  mismatch)
    printf "Hi someone-else! You've successfully authenticated, but GitHub does not provide shell access.\n" >&2
    exit 1 ;;
esac
printf "Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.\n" >&2
exit 1
SSHEOF

  cat >"$BIN/ssh-keyscan" <<'KEYSCANEOF'
#!/usr/bin/env bash
set -u
log_call() { local out="$LOG_DIR/$1.log"; shift; { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"; }
log_call ssh-keyscan "$@"
printf 'FAKE ssh-keyscan: trust-on-first-use fallback attempted\n' >&2
exit 99
KEYSCANEOF

  cat >"$BIN/ssh-keygen" <<'KEYGENEOF'
#!/usr/bin/env bash
set -u
log_call() { local out="$LOG_DIR/$1.log"; shift; { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"; }
log_call ssh-keygen "$@"
path=
list=0
while (($#)); do
  case "$1" in
    -f) path=$2; shift 2 ;;
    -l) list=1; shift ;;
    -N|-C|-t) shift 2 ;;
    -q) shift ;;
    *) shift ;;
  esac
done
if [[ -z $path ]]; then
  printf 'ssh-keygen: missing -f\n' >&2
  exit 1
fi
# -l -f <pub>: the fingerprint preflight reports for a candidate key; it
# must never touch the key files.
if [[ $list == 1 ]]; then
  printf '256 SHA256:FAKE0000000000000000000000000000000000000 codvps-github (ED25519)\n'
  exit 0
fi
printf 'Generating public/private ed25519 key pair.\n'
printf 'FAKE-PRIVATE-KEY %s\n' "$path" >"$path"
chmod 600 "$path"
printf 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFAKEKEYDATA codvps-github\n' >"$path.pub"
printf 'Your identification has been saved in %s\n' "$path"
printf 'Your public key has been saved in %s.pub\n' "$path"
printf 'The key fingerprint is:\nSHA256:FAKE0000000000000000000000000000000000000 codvps-github\n'
exit 0
KEYGENEOF

  cat >"$BIN/git" <<'GITEOF'
#!/usr/bin/env bash
set -u
log_call() { local out="$LOG_DIR/$1.log"; shift; { for a in "$@"; do printf '%s\x1f' "$a"; done; printf '\n'; } >>"$out"; }
log_call git "$@"
if [[ ${1-} == "clone" ]]; then
  printf 'FAKE GIT: clone attempted: %s\n' "$*" >&2
  exit 99
fi
if [[ ${1-} == "config" ]]; then
  shift
  args=()
  get=0
  for a in "$@"; do
    case "$a" in
      --global) : ;;
      --get) get=1 ;;
      *) args+=("$a") ;;
    esac
  done
  key=${args[0]-}
  store="$STATE_DIR/gitconfig-$key"
  if ((${#args[@]} >= 2)); then
    printf '%s' "${args[1]}" >"$store"
    exit 0
  fi
  if [[ $get == 1 && -f $store ]]; then
    cat "$store"
    exit 0
  fi
  exit 1
fi
exit 1
GITEOF

  chmod 0755 "$BIN"/gh "$BIN"/ssh "$BIN"/ssh-keygen "$BIN"/ssh-keyscan "$BIN"/git

  # The same fakes minus git, for the case that needs the real git.
  mkdir -p "$BIN_REALGIT"
  ln -s "$BIN/gh" "$BIN_REALGIT/gh"
  ln -s "$BIN/ssh" "$BIN_REALGIT/ssh"
  ln -s "$BIN/ssh-keygen" "$BIN_REALGIT/ssh-keygen"
  ln -s "$BIN/ssh-keyscan" "$BIN_REALGIT/ssh-keyscan"
}

expected_summary_line() {
  printf '  %-18s %-8s %s' "$1" "$2" "$3"
}

test_login_github() {
  local out rc
  set +e
  out=$(PATH="$BIN:$PATH" STATE_DIR="$STATE_DIR" LOG_DIR="$LOG_DIR" GH_TOKEN='' GITHUB_TOKEN='' \
    "$CODVPS_BIN" login github --non-interactive --key generate --passphrase-empty --yes \
    --identity-name X --identity-email x@example.invalid 2>&1)
  rc=$?
  set -e

  if [[ $rc -ne 0 ]]; then
    note "login github did not exit 0 (got $rc): $out"
  fi
  if ! grep -qFx "$(expected_summary_line gh_account ok 'logged in as octocat (keyring)')" <<<"$out"; then
    note "summary did not report gh_account ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line key ok "generated at $KEYPATH")" <<<"$out"; then
    note "summary did not report key ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line key_registration ok 'registered as codvps-octocat')" <<<"$out"; then
    note "summary did not report key_registration ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line known_hosts ok 'added 2 entr(y/ies)')" <<<"$out"; then
    note "summary did not report known_hosts ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line ssh_config ok "added Host github.com with IdentityFile $KEYPATH")" <<<"$out"; then
    note "summary did not report ssh_config ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line identity ok 'configured (X <x@example.invalid>)')" <<<"$out"; then
    note "summary did not report identity ok: $out"
  fi
  if ! grep -qFx "$(expected_summary_line ssh_auth ok 'authenticated as octocat')" <<<"$out"; then
    note "summary did not report ssh_auth ok: $out"
  fi
  if ! grep -qFx 'GitHub setup complete.' <<<"$out"; then
    note "summary did not report GitHub setup complete: $out"
  fi
}

test_key_file() {
  if [[ ! -f "$KEYPATH" ]]; then
    note "private key was not created at $KEYPATH"
    return
  fi
  if [[ "$(stat -c '%a' "$KEYPATH")" != "600" ]]; then
    note "private key mode is not 600: $(stat -c '%a' "$KEYPATH")"
  fi
  if [[ ! -f "$KEYPATH.pub" ]]; then
    note "public key was not created at $KEYPATH.pub"
  fi
}

test_known_hosts() {
  local kh="$HOME/.ssh/known_hosts"
  if ! grep -qFx 'github.com ssh-ed25519 AAAAFAKEEDKEYDATA' "$kh" 2>/dev/null; then
    note "known_hosts is missing the fake ed25519 github.com entry"
  fi
  if ! grep -qFx 'github.com ssh-rsa AAAAFAKERSAKEYDATA' "$kh" 2>/dev/null; then
    note "known_hosts is missing the fake rsa github.com entry"
  fi
}

test_ssh_config() {
  local cfg="$HOME/.ssh/config"
  if ! grep -qFx 'Host github.com' "$cfg" 2>/dev/null; then
    note "ssh config is missing the Host github.com block"
  fi
  if ! grep -qFx "  IdentityFile $KEYPATH" "$cfg" 2>/dev/null; then
    note "ssh config is missing the IdentityFile line"
  fi
  if ! grep -qFx '  IdentitiesOnly yes' "$cfg" 2>/dev/null; then
    note "ssh config is missing IdentitiesOnly yes"
  fi
}

test_no_clone_attempted() {
  local gitlog="$LOG_DIR/git.log"
  if [[ -f "$gitlog" ]] && cut -d $'\x1f' -f1 "$gitlog" | grep -qFx clone; then
    note "the fake git recorded a clone call during login github"
  fi
}

# ---------------------------------------------------------------------------
# Shared helpers for the cases below.

# argv_line <args...>: one call-log line, exactly as the fakes write it.
argv_line() {
  printf '%s\x1f' "$@"
}

# call_count <log> <args...>: how many logged calls had exactly this argv.
call_count() {
  local log=$1
  shift
  if [[ ! -f $log ]]; then
    echo 0
    return
  fi
  grep -cxF -- "$(argv_line "$@")" "$log" || true
}

# prefix_count <log> <args...>: how many logged calls began with this argv.
prefix_count() {
  local log=$1 prefix line n=0
  shift
  prefix=$(argv_line "$@")
  if [[ -f $log ]]; then
    while IFS= read -r line; do
      if [[ $line == "$prefix"* ]]; then
        n=$((n + 1))
      fi
    done <"$log"
  fi
  echo "$n"
}

sha_of() {
  sha256sum -- "$1" | cut -d' ' -f1
}

# new_case <name>: a scratch HOME, fake state and call logs for one case,
# logged in to the fake gh with a key-admin scope unless the case says
# otherwise.
new_case() {
  local dir="$WORK/cases/$1"
  CASE_HOME="$dir/home"
  CASE_STATE="$dir/state"
  CASE_LOG="$dir/logs"
  mkdir -p "$CASE_HOME/.ssh" "$CASE_STATE" "$CASE_LOG"
  chmod 0700 "$CASE_HOME/.ssh"
  : >"$CASE_STATE/logged-in"
}

# run_case <bin-dir> <login github flags...>: runs codvps login github for
# the current case with stdin at /dev/null (no tty), capturing combined
# output in $OUT and the exit status in $RC.
run_case() {
  local bin=$1
  shift
  set +e
  OUT=$(HOME="$CASE_HOME" PATH="$bin:$PATH" STATE_DIR="$CASE_STATE" LOG_DIR="$CASE_LOG" \
    GH_TOKEN='' GITHUB_TOKEN='' "$CODVPS_BIN" login github "$@" </dev/null 2>&1)
  RC=$?
  set -e
}

# Every flag a complete non-interactive run needs.
FULL_FLAGS=(--non-interactive --key generate --passphrase-empty --yes
  --identity-name X --identity-email x@example.invalid)

expect_rc() {
  [[ $RC -eq $1 ]] || note "$2: exit $RC, want $1: $OUT"
}

expect_summary() {
  grep -qFx -- "$(expected_summary_line "$1" "$2" "$3")" <<<"$OUT" ||
    note "$4: summary has no '$1 $2 $3' line: $OUT"
}

expect_text() {
  grep -qF -- "$1" <<<"$OUT" || note "$2: output lacks '$1': $OUT"
}

expect_no_summary() {
  ! grep -qFx 'GitHub setup summary:' <<<"$OUT" || note "$1: a hard failure still printed the summary: $OUT"
}

case_key() { printf '%s/.ssh/codvps_github_ed25519' "$CASE_HOME"; }

# ---------------------------------------------------------------------------
# Real operator $HOME, after test_login_github: a second identical run
# regenerates nothing, registers nothing again and leaves known_hosts alone.

test_rerun_is_idempotent() {
  local key_before pub_before kh_before out rc
  key_before=$(sha_of "$KEYPATH")
  pub_before=$(sha_of "$KEYPATH.pub")
  kh_before=$(sha_of "$HOME/.ssh/known_hosts")
  set +e
  out=$(PATH="$BIN:$PATH" STATE_DIR="$STATE_DIR" LOG_DIR="$LOG_DIR" GH_TOKEN='' GITHUB_TOKEN='' \
    "$CODVPS_BIN" login github --non-interactive --key generate --passphrase-empty --yes \
    --identity-name X --identity-email x@example.invalid 2>&1)
  rc=$?
  set -e
  OUT=$out
  RC=$rc
  expect_rc 0 "rerun"
  expect_summary key ok "already exists at $KEYPATH" "rerun"
  expect_summary key_registration ok 'already registered' "rerun"
  expect_summary known_hosts ok 'already up to date' "rerun"
  expect_summary ssh_config ok 'Host github.com block already present' "rerun"
  expect_summary identity ok 'already configured (X <x@example.invalid>)' "rerun"
  expect_summary ssh_auth ok 'authenticated as octocat' "rerun"
  [[ "$(sha_of "$KEYPATH")" == "$key_before" && "$(sha_of "$KEYPATH.pub")" == "$pub_before" ]] ||
    note "rerun changed the generated key pair"
  [[ "$(sha_of "$HOME/.ssh/known_hosts")" == "$kh_before" ]] || note "rerun changed known_hosts"
  [[ "$(prefix_count "$LOG_DIR/ssh-keygen.log" -t ed25519)" == 1 ]] ||
    note "ssh-keygen generated a key more than once across two runs"
  [[ "$(call_count "$LOG_DIR/gh.log" ssh-key add "$KEYPATH.pub" --title codvps-octocat)" == 1 ]] ||
    note "gh ssh-key add was not called exactly once across two runs"
  # The written Host block routes github.com to the key, so verify never
  # needs to pin it with -i.
  [[ "$(prefix_count "$LOG_DIR/ssh.log" -i)" == 0 ]] || note "verify pinned a key the ssh config already routes"
  [[ "$(call_count "$LOG_DIR/ssh.log" -T git@github.com)" == 2 ]] ||
    note "verify did not run a plain ssh -T git@github.com once per run"
}

# ---------------------------------------------------------------------------
# SSH key step.

test_key_no_choice_is_pending() {
  new_case no-key-choice
  run_case "$BIN" --non-interactive --yes --identity-name X --identity-email x@example.invalid
  expect_rc 2 "no --key choice"
  expect_summary key pending 'no --key choice given; pass --key skip|generate|existing:<path>' "no --key choice"
  expect_summary key_registration skipped 'no key selected' "no --key choice"
  expect_summary ssh_config skipped 'no dedicated key selected' "no --key choice"
  expect_summary ssh_auth skipped 'key setup is pending' "no --key choice"
  expect_text 'GitHub setup incomplete; rerun with the missing flags, or pass --defer to only recheck.' "no --key choice"
  [[ ! -e "$(case_key)" ]] || note "no --key choice still created a key"
  [[ ! -e $CASE_HOME/.ssh/config ]] || note "ssh config was written without a selected key"
  [[ "$(prefix_count "$CASE_LOG/ssh-keygen.log" -t)" == 0 ]] || note "no --key choice still ran ssh-keygen"
  [[ ! -e $CASE_LOG/ssh.log ]] || note "a pending key step still ran ssh"
}

test_generate_requires_passphrase_empty() {
  new_case generate-no-passphrase-flag
  run_case "$BIN" --non-interactive --key generate --yes --identity-name X --identity-email x@example.invalid
  expect_rc 1 "generate without --passphrase-empty"
  expect_text 'Error: generating a key non-interactively requires --passphrase-empty' "generate without --passphrase-empty"
  expect_no_summary "generate without --passphrase-empty"
  [[ ! -e "$(case_key)" && ! -e "$(case_key).pub" ]] || note "generate without --passphrase-empty created a key"
  [[ "$(prefix_count "$CASE_LOG/ssh-keygen.log" -t)" == 0 ]] || note "generate without --passphrase-empty ran ssh-keygen"
}

# ---------------------------------------------------------------------------
# Key registration step.

test_register_pending_without_yes() {
  new_case register-no-yes
  run_case "$BIN" --non-interactive --key generate --passphrase-empty --identity-name X --identity-email x@example.invalid
  expect_rc 2 "registration without --yes"
  expect_summary key ok "generated at $(case_key)" "registration without --yes"
  expect_summary key_registration pending 'registration requires --yes (non-interactive)' "registration without --yes"
  [[ "$(prefix_count "$CASE_LOG/gh.log" ssh-key add)" == 0 ]] || note "registration without --yes called gh ssh-key add"
}

test_register_stops_on_list_failure() {
  new_case register-list-fails
  : >"$CASE_STATE/ssh-key-list-fails"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 1 "gh ssh-key list failure"
  expect_text 'Error: could not check existing registration: gh ssh-key list failed' "gh ssh-key list failure"
  expect_no_summary "gh ssh-key list failure"
  [[ "$(prefix_count "$CASE_LOG/gh.log" ssh-key add)" == 0 ]] || note "a failed key listing still called gh ssh-key add"
}

test_register_requires_scope() {
  new_case register-scope
  : >"$CASE_STATE/scope-limited"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 2 "token without a key scope"
  expect_summary key_registration pending \
    'token for octocat lacks admin:public_key/write:public_key; run: gh auth refresh --scopes admin:public_key' \
    "token without a key scope"
  [[ "$(prefix_count "$CASE_LOG/gh.log" ssh-key add)" == 0 ]] || note "a scope-limited token still called gh ssh-key add"
}

# ---------------------------------------------------------------------------
# known_hosts step.

test_known_hosts_stops_without_meta() {
  new_case meta-fails
  : >"$CASE_STATE/meta-fails"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 1 "gh api meta failure"
  expect_text "Error: could not fetch GitHub's published host keys: gh api meta failed" "gh api meta failure"
  expect_no_summary "gh api meta failure"
  [[ ! -e $CASE_HOME/.ssh/known_hosts ]] || note "known_hosts was written although gh api meta failed"
  [[ ! -e $CASE_LOG/ssh-keyscan.log ]] || note "a gh api meta failure fell back to ssh-keyscan"
}

test_known_hosts_hashed_entries_recognized() {
  local kh before
  new_case hashed-known-hosts
  kh="$CASE_HOME/.ssh/known_hosts"
  # OpenSSH HashKnownHosts lines for github.com (fixed salt) carrying the
  # two host keys the fake gh api meta publishes.
  printf '%s\n' \
    '|1|OusAJGA4HG8ljoOV0wJvVw==|H9OWGJQ//0BgwOCz4qIN6kCCs+s= ssh-ed25519 AAAAFAKEEDKEYDATA' \
    '|1|OusAJGA4HG8ljoOV0wJvVw==|H9OWGJQ//0BgwOCz4qIN6kCCs+s= ssh-rsa AAAAFAKERSAKEYDATA' >"$kh"
  chmod 0600 "$kh"
  before=$(sha_of "$kh")
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 0 "hashed known_hosts"
  expect_summary known_hosts ok 'already up to date' "hashed known_hosts"
  [[ "$(sha_of "$kh")" == "$before" ]] || note "hashed known_hosts entries were rewritten"
}

test_known_hosts_refuses_conflicting_key() {
  local kh before
  new_case conflicting-known-hosts
  kh="$CASE_HOME/.ssh/known_hosts"
  printf 'github.com ssh-rsa AAAABOGUSDATANOTPUBLISHED\n' >"$kh"
  chmod 0600 "$kh"
  before=$(sha_of "$kh")
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 1 "conflicting known_hosts key"
  expect_text "known_hosts has a github.com key GitHub does not publish; refusing to touch the file" "conflicting known_hosts key"
  expect_no_summary "conflicting known_hosts key"
  [[ "$(sha_of "$kh")" == "$before" ]] || note "a conflicting known_hosts file was modified"
}

# ---------------------------------------------------------------------------
# Git identity step (the fake git keeps --global config in $CASE_STATE).

test_identity_never_overwritten() {
  new_case identity-existing
  printf 'Existing Name' >"$CASE_STATE/gitconfig-user.name"
  printf 'existing@example.invalid' >"$CASE_STATE/gitconfig-user.email"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 0 "existing identity"
  expect_summary identity ok 'already configured (Existing Name <existing@example.invalid>)' "existing identity"
  [[ "$(cat "$CASE_STATE/gitconfig-user.name")" == 'Existing Name' &&
    "$(cat "$CASE_STATE/gitconfig-user.email")" == 'existing@example.invalid' ]] ||
    note "an existing git identity was overwritten"
  [[ "$(prefix_count "$CASE_LOG/git.log" config --global user.name)" == 0 &&
    "$(prefix_count "$CASE_LOG/git.log" config --global user.email)" == 0 ]] ||
    note "login github wrote git config over an existing identity"
}

test_identity_pending_without_flags() {
  new_case identity-no-flags
  run_case "$BIN" --non-interactive --key generate --passphrase-empty --yes
  expect_rc 2 "no identity and no flags"
  expect_summary identity pending 'missing user.name (--identity-name), user.email (--identity-email)' "no identity and no flags"
  [[ ! -e $CASE_STATE/gitconfig-user.name && ! -e $CASE_STATE/gitconfig-user.email ]] ||
    note "git config was written with no identity flags"
  [[ "$(prefix_count "$CASE_LOG/git.log" config --global user.name)" == 0 &&
    "$(prefix_count "$CASE_LOG/git.log" config --global user.email)" == 0 ]] ||
    note "login github wrote git config with no identity flags"
}

test_identity_derives_noreply_email() {
  new_case identity-name-only
  run_case "$BIN" --non-interactive --key generate --passphrase-empty --yes --identity-name X
  expect_rc 0 "--identity-name only"
  expect_summary identity ok 'configured (X <999+octocat@users.noreply.github.com>)' "--identity-name only"
  [[ "$(cat "$CASE_STATE/gitconfig-user.email" 2>/dev/null)" == '999+octocat@users.noreply.github.com' ]] ||
    note "the derived noreply email was not written to git config"
}

# ---------------------------------------------------------------------------
# SSH verification step.

test_verify_fails_on_publickey_error() {
  new_case verify-publickey
  printf 'publickey' >"$CASE_STATE/ssh-mode"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 1 "ssh publickey error"
  expect_text 'Error: ssh verification failed: git@github.com: Permission denied (publickey).' "ssh publickey error"
  expect_no_summary "ssh publickey error"
}

test_verify_fails_on_account_mismatch() {
  new_case verify-mismatch
  printf 'mismatch' >"$CASE_STATE/ssh-mode"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 1 "ssh account mismatch"
  expect_text 'Error: ssh verification connected as "someone-else", expected the gh account "octocat"' "ssh account mismatch"
  expect_no_summary "ssh account mismatch"
}

test_verify_pins_selected_key() {
  local key
  new_case verify-pin
  key="$CASE_HOME/.ssh/id_pin"
  printf 'FAKE-PRIVATE-KEY pin\n' >"$key"
  printf 'ssh-ed25519 AAAAFAKEPINKEYDATA pin\n' >"$key.pub"
  chmod 0600 "$key"
  # A Host block that routes github.com without naming the selected key:
  # ensure_ssh_config leaves it alone, so verify must pin the key itself.
  printf 'Host github.com\n  User git\n' >"$CASE_HOME/.ssh/config"
  chmod 0600 "$CASE_HOME/.ssh/config"
  run_case "$BIN" --non-interactive --key "existing:$key" --yes --identity-name X --identity-email x@example.invalid
  expect_rc 0 "existing key without a routing Host block"
  expect_summary key ok "using existing key at $key" "existing key without a routing Host block"
  expect_summary ssh_config ok 'Host github.com block already present' "existing key without a routing Host block"
  expect_summary ssh_auth ok 'authenticated as octocat' "existing key without a routing Host block"
  [[ "$(call_count "$CASE_LOG/ssh.log" -i "$key" -o IdentitiesOnly=yes -T git@github.com)" == 1 ]] ||
    note "verify did not pin the selected key with -i/-o IdentitiesOnly=yes: $(cat "$CASE_LOG/ssh.log" 2>/dev/null)"
}

# ---------------------------------------------------------------------------
# gh authentication step.

# assert_nothing_written <label>: a logged-out run stopped before any key,
# known_hosts, ssh config or git identity write, and never ran gh auth login.
assert_nothing_written() {
  [[ ! -e "$(case_key)" ]] || note "$1: a key was generated before authentication"
  [[ ! -e $CASE_HOME/.ssh/known_hosts ]] || note "$1: known_hosts was written before authentication"
  [[ ! -e $CASE_HOME/.ssh/config ]] || note "$1: ssh config was written before authentication"
  [[ "$(prefix_count "$CASE_LOG/git.log" config --global user.name)" == 0 &&
    "$(prefix_count "$CASE_LOG/git.log" config --global user.email)" == 0 ]] ||
    note "$1: git identity was written before authentication"
  [[ "$(prefix_count "$CASE_LOG/gh.log" auth login)" == 0 ]] || note "$1: gh auth login was called"
}

test_auth_non_interactive_logged_out() {
  new_case logged-out-non-interactive
  rm -f "$CASE_STATE/logged-in"
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 2 "--non-interactive while logged out"
  expect_text 'Error: not logged in to github.com; run: gh auth login' "--non-interactive while logged out"
  expect_no_summary "--non-interactive while logged out"
  assert_nothing_written "--non-interactive while logged out"
}

#  with no terminal at all (stdin /dev/null, as docker exec without
# -t or ssh without -t), a logged-out run starts GitHub's device flow, shows
# the code and URL, takes every prompt's default, and completes.
test_auth_no_tty_logged_out_runs_device_flow() {
  new_case logged-out-no-tty
  rm -f "$CASE_STATE/logged-in"
  run_case "$BIN" --passphrase-empty --yes --identity-name X --identity-email x@example.invalid
  expect_rc 0 "no tty while logged out"
  expect_text 'First copy your one-time code: ABCD-1234' "no tty while logged out"
  expect_text 'https://github.com/login/device' "no tty while logged out"
  expect_text "assuming yes: generate SSH key at $(case_key) (--yes)" "no tty while logged out"
  expect_summary gh_account ok 'logged in as octocat (keyring)' "no tty while logged out"
  expect_text 'GitHub setup complete.' "no tty while logged out"
  [[ "$(prefix_count "$CASE_LOG/gh.log" auth login)" == 1 ]] || note "no tty while logged out: gh auth login was not called once"
}

test_auth_already_logged_in_no_relogin() {
  new_case already-logged-in
  run_case "$BIN" "${FULL_FLAGS[@]}"
  expect_rc 0 "logged in, --non-interactive"
  expect_summary gh_account ok 'logged in as octocat (keyring)' "logged in, --non-interactive"
  run_case "$BIN" --key generate --passphrase-empty --yes --identity-name X --identity-email x@example.invalid
  expect_rc 0 "logged in, interactive without a tty"
  expect_summary gh_account ok 'logged in as octocat (keyring)' "logged in, interactive without a tty"
  [[ "$(prefix_count "$CASE_LOG/gh.log" auth login)" == 0 ]] || note "an already logged-in run called gh auth login"
}

# ---------------------------------------------------------------------------
# tests/smoke.sh test_git_identity_configured: the identity login github
# sets is the one a plain git commit then records. This case uses the real
# git (only gh/ssh/ssh-keygen are faked) under its scratch HOME.

test_git_identity_used_by_commit() {
  local repo author
  new_case git-identity
  run_case "$BIN_REALGIT" --non-interactive --key skip \
    --identity-name 'Fakevps Identity' --identity-email identity@example.invalid
  expect_rc 0 "--key skip with an identity"
  expect_summary key skipped 'skipped by --key skip' "--key skip with an identity"
  expect_summary ssh_config skipped 'no dedicated key selected' "--key skip with an identity"
  expect_summary identity ok 'configured (Fakevps Identity <identity@example.invalid>)' "--key skip with an identity"
  expect_summary ssh_auth skipped 'key setup is skipped' "--key skip with an identity"
  expect_text 'GitHub setup complete.' "--key skip with an identity"
  [[ ! -e $CASE_HOME/.ssh/config ]] || note "--key skip wrote an ssh config"

  repo="$WORK/cases/git-identity/repo"
  HOME="$CASE_HOME" git init -q -b main "$repo"
  printf 'identity smoke\n' >"$repo/README.md"
  HOME="$CASE_HOME" git -C "$repo" add README.md
  HOME="$CASE_HOME" git -C "$repo" commit -q -m 'Identity smoke'
  author=$(HOME="$CASE_HOME" git -C "$repo" log -1 --format='%an <%ae>')
  [[ $author == 'Fakevps Identity <identity@example.invalid>' ]] ||
    note "a commit after login github recorded author '$author'"
}

setup
test_login_github
test_key_file
test_known_hosts
test_ssh_config
test_no_clone_attempted
test_rerun_is_idempotent
test_key_no_choice_is_pending
test_generate_requires_passphrase_empty
test_register_pending_without_yes
test_register_stops_on_list_failure
test_register_requires_scope
test_known_hosts_stops_without_meta
test_known_hosts_hashed_entries_recognized
test_known_hosts_refuses_conflicting_key
test_identity_never_overwritten
test_identity_pending_without_flags
test_identity_derives_noreply_email
test_verify_fails_on_publickey_error
test_verify_fails_on_account_mismatch
test_verify_pins_selected_key
test_auth_non_interactive_logged_out
test_auth_no_tty_logged_out_runs_device_flow
test_auth_already_logged_in_no_relogin
test_git_identity_used_by_commit

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more login github assertions failed"
  exit 1
fi
echo "PASS: login github completes non-interactively, is idempotent, and every step reports, refuses or stays pending exactly as specified"
