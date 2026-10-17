#!/usr/bin/env bash
set -euo pipefail

# Test: codvps repo add|list|remove
#
# Covers (see docs/test-parity-repo.md for the full mapping):
#   - test_fixture_add_without_tty                            (tests/smoke.sh)
#   - test_existing_checkout_can_be_reattached_only_to_matching_remote (tests/smoke.sh)
#   - test_list_and_status                                     (tests/smoke.sh)
#   - test_repo_remove_retains_checkout                        (tests/smoke.sh)
#   - test_repo_membership_rejects_symlinked_physical_repo     (tests/smoke.sh)
#   - test_bad_names_rejected                                  (tests/smoke.sh)
#   - test_registry_is_the_membership_authority                (tests/smoke.sh)
#   - test_repo_add_rolls_back_clean_clone_after_membership_failure (tests/smoke.sh)
#   - test_registry_prunes_a_deleted_repository                (tests/smoke.sh)
#   - test_stray_clone_under_home_is_not_a_repository          (tests/smoke.sh)
#   - test_unregistered_directory_cannot_be_managed            (tests/smoke.sh)
#   - test_linked_worktree_is_not_a_second_repository          (tests/smoke.sh)
#
# The reference's registry cases also checked the Codex head's mount policy
# and doctor. The mount policy and membership manifest were removed by owner
# decision (Codex Remote is not sandboxed by codvps); 16_head_codex.sh
# checks that repo add/remove leave the Codex daemon alone.
# Here the Claude head stays disabled (repo list shows claude=disabled), so
# no repository may ever acquire an enabled Claude unit.
#
# `repo remove` exercises real systemd (DisableClaudeUnit), which needs a
# reachable operator `systemctl --user` session backed by a loaded
# claude-remote@.service (the product template, installed by the Dockerfile under
# /etc/systemd/user/, which every user's --user manager searches). This
# scenario brings up that session itself with loginctl/user@.service; see
# the final report for why this is the one part of this scenario that needs
# CI confirmation.

WORK="$HOME/tmp/repo13"
SEED="$WORK/seed"
REMOTE="$WORK/remote.git"
NAME=remote
REGISTRY="$HOME/.config/codvps/repositories"
DEST="$HOME/$NAME"
REGISTRY_HELPER="$(dirname "${BASH_SOURCE[0]}")/registry.py"
EXTERNAL_ROOT=""
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

cleanup() {
  if [[ -n $EXTERNAL_ROOT ]]; then
    "$CODVPS_BIN" config unset repo_root >/dev/null 2>&1 || true
    rm -rf "$EXTERNAL_ROOT"
  fi
}
trap cleanup EXIT

setup() {
  rm -rf "$WORK" "$DEST" "$HOME/linked" "$HOME/rollback13" "$HOME/prune13" "$HOME/stray13" \
    "$HOME/not-a-repo" "$HOME/remote-wt"
  rm -f "$HOME/.config/codvps/repositories" "$HOME/.config/codvps/codex-repositories"
  mkdir -p "$WORK"

  git init -q -b main "$SEED"
  git -C "$SEED" config user.email "fakevps@example.invalid"
  git -C "$SEED" config user.name "Fakevps Test"
  echo hi >"$SEED/README.md"
  git -C "$SEED" add -A
  git -C "$SEED" commit -q -m init
  git init -q --bare "$REMOTE"
  git -C "$SEED" push -q "$REMOTE" HEAD:main

  # Bring up a reachable `systemctl --user` session for operator, so
  # `repo remove`'s DisableClaudeUnit call (systemctl --user disable/stop
  # against the product claude-remote@.service template) has a real bus to
  # talk to instead of failing on a missing session.
  sudo loginctl enable-linger operator >/dev/null 2>&1 || true
  sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
}

test_bad_name_rejected() {
  local rc
  set +e
  "$CODVPS_BIN" repo add '/tmp/does-not-matter/bad!name.git' >/dev/null 2>&1
  rc=$?
  set -e
  if [[ $rc -ne 1 ]]; then
    note "repo add with a bad name did not exit 1 (got $rc)"
  fi
  if [[ -e "$HOME/bad!name" ]]; then
    note "repo add with a bad name created a checkout anyway"
  fi
}

test_symlinked_checkout_rejected() {
  ln -s /tmp "$HOME/linked"
  local rc
  set +e
  "$CODVPS_BIN" repo add '/nonexistent/path/linked.git' >/dev/null 2>&1
  rc=$?
  set -e
  if [[ $rc -ne 1 ]]; then
    note "repo add onto a symlinked checkout path did not exit 1 (got $rc)"
  fi
  if [[ ! -L "$HOME/linked" ]]; then
    note "repo add replaced the pre-existing symlink at \$HOME/linked instead of refusing"
  fi
  rm -f "$HOME/linked"
}

test_add() {
  local out rc
  set +e
  out=$("$CODVPS_BIN" repo add "$REMOTE" 2>&1)
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    note "repo add did not exit 0 (got $rc): $out"
  fi
  if ! grep -qFx "Repository added: $REMOTE" <<<"$out"; then
    note "repo add did not report the expected message: $out"
  fi
  if [[ ! -d "$DEST/.git" ]]; then
    note "repo add did not create a git checkout at $DEST"
  fi
  if ! registry_has "$NAME" "$DEST"; then
    note "repo add did not register $NAME in the repositories file"
  fi
}

test_readd_is_idempotent() {
  local out rc count
  set +e
  out=$("$CODVPS_BIN" repo add "$REMOTE" 2>&1)
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    note "re-adding the same repository did not exit 0 (got $rc): $out"
  fi
  if ! grep -qFx "Repository added: $REMOTE" <<<"$out"; then
    note "re-add did not report the expected message: $out"
  fi
  count=$(python3 "$REGISTRY_HELPER" "$REGISTRY" count "$NAME" "$DEST")
  if [[ $count -ne 1 ]]; then
    note "re-adding the same repository produced $count registry entries, want 1"
  fi
}

test_list_exact() {
  local out expected_codex_state expected_running expected
  expected_codex_state="$(systemctl is-enabled codex-remote@operator.service 2>/dev/null || true)"
  [[ -z "$expected_codex_state" ]] && expected_codex_state=unknown
  if systemctl is-active --quiet codex-remote@operator.service 2>/dev/null; then
    expected_running=running
  else
    expected_running=unavailable
  fi

  # $(...) strips trailing newlines from $out, so expected must end without
  # one too (it otherwise matches the Fprintf format byte-for-byte).
  expected="Heads: claude=disabled codex=${expected_codex_state}/${expected_running}"$'\n\n'
  expected+="$(printf '%-24s %-40s %s' REPOSITORY PATH CLAUDE_HEAD)"$'\n'
  expected+="$(printf '%-24s %-40s %s' "$NAME" "$DEST" -)"

  out=$("$CODVPS_BIN" repo list)
  if [[ "$out" != "$expected" ]]; then
    note "repo list did not match exactly.
--- expected ---
$expected
--- got ---
$out"
  fi
}

test_remove_keeps_checkout() {
  local out rc
  set +e
  out=$("$CODVPS_BIN" repo remove "$NAME" 2>&1)
  rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    note "repo remove did not exit 0 (got $rc): $out"
  fi
  if ! grep -qFx "Repository removed from registry: $NAME" <<<"$out"; then
    note "repo remove did not report the expected message: $out"
  fi
  if [[ ! -d "$DEST/.git" ]]; then
    note "repo remove deleted the working checkout at $DEST"
  fi
  if registry_has "$NAME"; then
    note "repo remove left $NAME registered"
  fi
}

# fixture <name>: a bare repository $WORK/<name>.git with one commit.
fixture() {
  local src="$WORK/$1-src"
  git init -q -b main "$src"
  git -C "$src" config user.email "fakevps@example.invalid"
  git -C "$src" config user.name "Fakevps Test"
  printf '# %s\n' "$1" >"$src/README.md"
  git -C "$src" add -A
  git -C "$src" commit -q -m init
  git init -q --bare "$WORK/$1.git"
  git -C "$src" push -q "$WORK/$1.git" HEAD:main
}

# run <cmd...>: combined output in $OUT, exit status in $RC.
run() {
  set +e
  OUT=$("$@" 2>&1)
  RC=$?
  set -e
}

registry_has() { python3 "$REGISTRY_HELPER" "$REGISTRY" has "$@"; }
list_has_row() { grep -q "^$1 " <<<"$OUT"; }
claude_unit_enabled() { systemctl --user is-enabled --quiet "claude-remote@$1.service" 2>/dev/null; }

# The registry is an operator-owned 0600 file naming the repository,
# registering writes no Codex membership manifest, and registering
# never enables a Claude unit.
test_registry_is_the_membership_authority() {
  [[ "$(stat -c '%U:%G:%a' "$HOME/.config/codvps/repositories")" == "$(id -un):$(id -gn):600" ]] ||
    note "the registry is not an operator-owned 0600 file: $(stat -c '%U:%G:%a' "$HOME/.config/codvps/repositories")"
  registry_has "$NAME" || note "the registry does not name $NAME"
  [[ ! -e $HOME/.config/codvps/codex-repositories ]] || note "registering $NAME wrote a Codex membership manifest"
  ! claude_unit_enabled "$NAME" || note "registering $NAME enabled its Claude unit with the Claude head disabled"
}

# A failed registry write rolls back the clean clone it just made.
test_repo_add_rolls_back_clean_clone() {
  local config="$HOME/.config/codvps" mode
  fixture rollback13
  mode=$(stat -c '%a' "$config")
  chmod 0500 "$config"
  run "$CODVPS_BIN" repo add "$WORK/rollback13.git"
  chmod "$mode" "$config"
  [[ $RC -eq 1 ]] || note "repo add with an unwritable registry did not exit 1 (got $RC): $OUT"
  grep -qF 'codvps: failed to update registry:' <<<"$OUT" || note "repo add did not report the registry failure: $OUT"
  [[ ! -e $HOME/rollback13 && ! -L $HOME/rollback13 ]] || note "the clean clone was left behind after the registry failure"
  ! registry_has rollback13 || note "a rolled-back add is registered"
}

# Deleting a registered checkout deregisters it on the next repo list.
test_registry_prunes_a_deleted_repository() {
  fixture prune13
  run "$CODVPS_BIN" repo add "$WORK/prune13.git"
  [[ $RC -eq 0 ]] || note "repo add prune13 failed (rc=$RC): $OUT"
  registry_has prune13 || note "repo add did not register prune13"
  rm -rf "$HOME/prune13"
  run "$CODVPS_BIN" repo list
  [[ $RC -eq 0 ]] || note "repo list after deleting a checkout failed (rc=$RC): $OUT"
  ! list_has_row prune13 || note "repo list still shows the deleted prune13: $OUT"
  list_has_row "$NAME" || note "pruning prune13 dropped the unrelated $NAME: $OUT"
  ! registry_has prune13 || note "repo list did not prune prune13 from the registry"
  ! claude_unit_enabled prune13 || note "a pruned repository kept an enabled Claude unit"
}

# A clone codvps did not register is invisible to it.
test_stray_clone_is_not_a_repository() {
  fixture stray13
  git clone -q "$WORK/stray13.git" "$HOME/stray13"
  run "$CODVPS_BIN" repo list
  [[ $RC -eq 0 ]] || note "repo list with a stray clone failed (rc=$RC): $OUT"
  ! list_has_row stray13 || note "a stray clone appeared in repo list: $OUT"
  ! registry_has stray13 || note "a stray clone was adopted into the registry"
  ! claude_unit_enabled stray13 || note "a stray clone acquired a Claude unit"
  rm -rf "$HOME/stray13"
}

# A plain directory cannot be added, and removing it by name only cleans up
# the orphaned Claude unit someone enabled for it.
test_unregistered_directory_cannot_be_managed() {
  mkdir -p "$HOME/not-a-repo"
  systemctl --user enable claude-remote@not-a-repo.service >/dev/null 2>&1 ||
    note "could not enable the orphaned claude-remote@not-a-repo.service fixture"
  run "$CODVPS_BIN" repo add "$WORK/not-a-repo.git"
  [[ $RC -eq 1 ]] || note "repo add over a plain directory did not exit 1 (got $RC): $OUT"
  [[ $OUT == *"failed to inspect Git directory for \"$HOME/not-a-repo\""* && $OUT == *"$HOME/not-a-repo/.git: no such file or directory"* ]] ||
    note "repo add did not refuse the plain directory's missing Git directory: $OUT"
  ! registry_has not-a-repo || note "a plain directory was registered"
  run "$CODVPS_BIN" repo remove not-a-repo
  [[ $RC -eq 0 ]] || note "repo remove not-a-repo failed (rc=$RC): $OUT"
  run "$CODVPS_BIN" repo remove not-a-repo
  [[ $RC -eq 0 ]] || note "a second repo remove not-a-repo failed (rc=$RC): $OUT"
  ! claude_unit_enabled not-a-repo || note "repo remove left the orphaned Claude unit enabled"
  [[ -d $HOME/not-a-repo ]] || note "repo remove deleted the plain directory"
  rmdir "$HOME/not-a-repo"
}

# A linked worktree forged into the registry fails closed. Listing must leave
# the registry and checkout intact; only explicit removal drops the bad entry.
test_linked_worktree_is_not_a_second_repository() {
  local before="$WORK/registry-before-worktree-list"
  run "$CODVPS_BIN" repo add "$REMOTE"
  [[ $RC -eq 0 ]] || note "re-adding $NAME failed (rc=$RC): $OUT"
  git -C "$DEST" worktree add -q -b wt13 "$HOME/remote-wt" origin/main
  [[ -f $HOME/remote-wt/.git ]] || note "git did not create a linked worktree with a .git file"
  python3 "$REGISTRY_HELPER" "$REGISTRY" add remote-wt "$HOME/remote-wt"
  cp "$REGISTRY" "$before"
  run "$CODVPS_BIN" repo list
  [[ $RC -ne 0 ]] || note "repo list accepted a forged linked-worktree entry: $OUT"
  [[ $OUT == *"cannot validate registered checkout \"$HOME/remote-wt\""* && $OUT == *'linked worktree or submodule (.git is not a real directory)'* ]] ||
    note "repo list did not identify the invalid linked-worktree entry: $OUT"
  ! list_has_row remote-wt || note "a linked worktree appeared in repo list: $OUT"
  cmp -s "$REGISTRY" "$before" || note "a failed linked-worktree validation changed the registry"
  registry_has remote-wt "$HOME/remote-wt" || note "repo list silently dropped the invalid linked-worktree registration"
  ! claude_unit_enabled remote-wt || note "a linked worktree acquired a Claude unit"
  [[ -d $HOME/remote-wt ]] || note "codvps deleted a linked worktree it refused to manage"
  run "$CODVPS_BIN" repo remove remote-wt
  [[ $RC -eq 0 ]] || note "explicitly removing the invalid worktree entry failed (rc=$RC): $OUT"
  ! registry_has remote-wt || note "explicit removal left the invalid worktree entry registered"
  registry_has "$NAME" "$DEST" || note "removing the worktree entry changed its primary repository's registration"
  [[ -f $HOME/remote-wt/.git ]] || note "deregistering the worktree deleted its checkout"
  run "$CODVPS_BIN" repo list
  [[ $RC -eq 0 ]] || note "repo list did not recover after removing the invalid worktree entry: $OUT"
  list_has_row "$NAME" || note "repo list lost the primary repository after worktree deregistration: $OUT"
  git -C "$DEST" worktree remove --force "$HOME/remote-wt"
  git -C "$DEST" branch -q -D wt13
  run "$CODVPS_BIN" repo remove "$NAME"
  [[ $RC -eq 0 ]] || note "final repo remove $NAME failed (rc=$RC): $OUT"
}

# A real legacy checkout stays under HOME when an external root is configured.
# New adds persist explicit paths, including an override whose basename differs
# from its registered name. Changing or unsetting the root never remaps either.
test_configured_and_explicit_checkout_paths() {
  local root explicit name
  EXTERNAL_ROOT=$(mktemp -d '/tmp/codvps repo13.XXXXXX')
  root="$EXTERNAL_ROOT/root one"
  explicit="$EXTERNAL_ROOT/different checkout name"
  fixture external13
  fixture explicit13
  printf '%s\n' "$NAME" >"$REGISTRY"
  chmod 0600 "$REGISTRY"

  run "$CODVPS_BIN" config set repo_root "$root"
  [[ $RC -eq 0 ]] || note "config set repo_root failed (rc=$RC): $OUT"
  run "$CODVPS_BIN" config get repo_root
  [[ $RC -eq 0 && $OUT == "$root" ]] || note "config get did not return the external root: $OUT"
  [[ "$(stat -c '%U:%a' "$HOME/.config/codvps/settings.json")" == "$(id -un):600" ]] ||
    note "settings are not operator-owned mode 600"

  run "$CODVPS_BIN" repo list
  [[ $RC -eq 0 ]] || note "listing a legacy registration with an external root failed: $OUT"
  grep -Fxq "$(printf '%-24s %-40s %s' "$NAME" "$DEST" -)" <<<"$OUT" ||
    note "the legacy registration moved away from HOME: $OUT"

  run "$CODVPS_BIN" repo add "$WORK/external13.git"
  [[ $RC -eq 0 ]] || note "adding under the configured root failed (rc=$RC): $OUT"
  [[ -d "$root/external13/.git" && ! -e "$HOME/external13" ]] ||
    note "the configured root was not used for external13"
  registry_has external13 "$root/external13" || note "external13 has no explicit registered path"
  registry_has "$NAME" || note "JSON migration lost the legacy registration"

  run "$CODVPS_BIN" repo add "$WORK/explicit13.git" --dest "$explicit"
  [[ $RC -eq 0 ]] || note "repo add --dest failed (rc=$RC): $OUT"
  [[ -d "$explicit/.git" && ! -e "$root/explicit13" ]] || note "--dest did not override repo_root"
  registry_has explicit13 "$explicit" || note "--dest was not persisted by registered name"

  run "$CODVPS_BIN" config set repo_root "$EXTERNAL_ROOT/root two"
  [[ $RC -eq 0 ]] || note "changing repo_root failed (rc=$RC): $OUT"
  for name in remote external13 explicit13; do
    run "$CODVPS_BIN" repo add "$WORK/$name.git"
    [[ $RC -eq 0 ]] || note "re-adding $name after changing repo_root failed: $OUT"
  done
  [[ ! -e "$EXTERNAL_ROOT/root two" ]] || note "changing repo_root moved or duplicated a checkout"
  registry_has external13 "$root/external13" || note "changing repo_root remapped external13"
  registry_has explicit13 "$explicit" || note "changing repo_root remapped explicit13"

  run "$CODVPS_BIN" repo add "$WORK/explicit13.git" --dest "$EXTERNAL_ROOT/remapped"
  [[ $RC -ne 0 && $OUT == *'refusing to move or remap'* ]] || note "an explicit remap was not refused: $OUT"
  [[ ! -e "$EXTERNAL_ROOT/remapped" ]] || note "a refused remap created a checkout"

  run "$CODVPS_BIN" config unset repo_root
  [[ $RC -eq 0 ]] || note "config unset repo_root failed (rc=$RC): $OUT"
  run "$CODVPS_BIN" config get repo_root
  [[ $RC -eq 0 && $OUT == "$HOME" ]] || note "unsetting repo_root did not restore the HOME default: $OUT"
  run "$CODVPS_BIN" repo list
  [[ $RC -eq 0 ]] || note "listing persisted paths after unsetting repo_root failed: $OUT"
  for name in "$NAME" external13 explicit13; do
    list_has_row "$name" || note "repo list lost $name after unsetting repo_root: $OUT"
  done
  grep -Fxq "$(printf '%-24s %-40s %s' "$NAME" "$DEST" -)" <<<"$OUT" ||
    note "the legacy path changed during JSON migration: $OUT"
  grep -Fxq "$(printf '%-24s %-40s %s' external13 "$root/external13" -)" <<<"$OUT" ||
    note "repo list did not retain the configured checkout path: $OUT"
  grep -Fxq "$(printf '%-24s %-40s %s' explicit13 "$explicit" -)" <<<"$OUT" ||
    note "repo list did not retain the explicit checkout path: $OUT"

  for name in "$NAME" external13 explicit13; do
    run "$CODVPS_BIN" repo remove "$name"
    [[ $RC -eq 0 ]] || note "removing $name failed (rc=$RC): $OUT"
    ! registry_has "$name" || note "removing $name left its registration"
  done
  [[ -d "$DEST/.git" && -d "$root/external13/.git" && -d "$explicit/.git" ]] ||
    note "removing registrations deleted a legacy or external checkout"
}

setup
test_bad_name_rejected
test_symlinked_checkout_rejected
test_add
test_readd_is_idempotent
test_list_exact
test_registry_is_the_membership_authority
test_repo_add_rolls_back_clean_clone
test_registry_prunes_a_deleted_repository
test_stray_clone_is_not_a_repository
test_remove_keeps_checkout
test_unregistered_directory_cannot_be_managed
test_linked_worktree_is_not_a_second_repository
test_configured_and_explicit_checkout_paths

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more repo assertions failed"
  exit 1
fi
echo "PASS: repo add/list/remove behave as expected, legacy and external paths stay pinned, and remove keeps the checkout"
