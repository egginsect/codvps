#!/usr/bin/env bash
set -euo pipefail

# Test: codvps config link <repo-url> and the hidden config unlink.
#
# config link registers the config repo like `repo add`, then replaces each
# mirrored home file with a symlink into the checkout, so git in the checkout
# is the history. Covers: first link, backup of a differing home file,
# refusal of repo credentials, rerun no-op, a home edit showing up in
# `git status`, doctor's drift warning, and unlink restoring regular files.

WORK="$HOME/tmp/cfglink"
SEED="$WORK/seed"
REMOTE="$WORK/agentcfg.git"
CHECKOUT="$HOME/agentcfg"
BACKUP="$HOME/.config/codvps/config-link-backup"
FAILED=0

note() { printf 'FAIL: %s\n' "$1"; FAILED=1; }

# shellcheck disable=SC2317
cleanup() {
  "$CODVPS_BIN" config unlink >/dev/null 2>&1 || true
  "$CODVPS_BIN" repo remove agentcfg >/dev/null 2>&1 || true
  rm -rf "$WORK" "$CHECKOUT" "$BACKUP" "$HOME/.claude/CLAUDE.md" "$HOME/.claude/skills/demo" \
    "$HOME/.codex/AGENTS.md"
}
trap cleanup EXIT

setup() {
  cleanup
  mkdir -p "$WORK"
  # config link registers the repo like repo add, which attaches heads over
  # the operator's systemd --user bus; bring that session up (as 13_repo.sh).
  sudo loginctl enable-linger operator >/dev/null 2>&1 || true
  sudo systemctl start "user@$(id -u)".service >/dev/null 2>&1 || true
  XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export XDG_RUNTIME_DIR
  git init -q -b main "$SEED"
  git -C "$SEED" config user.email "fakevps@example.invalid"
  git -C "$SEED" config user.name "Fakevps Test"
  mkdir -p "$SEED/claude/skills/demo" "$SEED/codex"
  echo "repo rules" >"$SEED/claude/CLAUDE.md"
  echo "skill" >"$SEED/claude/skills/demo/SKILL.md"
  echo "codex rules" >"$SEED/codex/AGENTS.md"
  git -C "$SEED" add -A
  git -C "$SEED" commit -q -m init
  git init -q --bare -b main "$REMOTE"
  git -C "$SEED" push -q "$REMOTE" HEAD:main

  # A home file that differs from the repo's copy must be backed up.
  mkdir -p "$HOME/.claude"
  echo "local rules" >"$HOME/.claude/CLAUDE.md"
}

test_link() {
  local out
  if ! out=$("$CODVPS_BIN" config link "$REMOTE" 2>&1); then
    note "config link failed: $out"
    return
  fi
  [[ "$out" == *"Linked config repo ~/agentcfg: 3 linked, 0 already linked, 1 backed up, 0 refused."* ]] ||
    note "unexpected link summary: $out"
  [[ "$(readlink "$HOME/.claude/CLAUDE.md")" == "$CHECKOUT/claude/CLAUDE.md" ]] || note "$HOME/.claude/CLAUDE.md is not linked"
  [[ "$(readlink "$HOME/.claude/skills/demo/SKILL.md")" == "$CHECKOUT/claude/skills/demo/SKILL.md" ]] || note "skill is not linked"
  [[ "$(readlink "$HOME/.codex/AGENTS.md")" == "$CHECKOUT/codex/AGENTS.md" ]] || note "$HOME/.codex/AGENTS.md is not linked"
  [[ "$(cat "$BACKUP/.claude/CLAUDE.md" 2>/dev/null)" == "local rules" ]] || note "differing home file was not backed up"
  "$CODVPS_BIN" repo list 2>/dev/null | grep -q agentcfg || note "config repo is not registered"
  [[ $FAILED -eq 0 ]] && echo "PASS: config link registers the repo and links every mirrored file"
}

test_rerun_is_noop() {
  local out
  out=$("$CODVPS_BIN" config link agentcfg 2>&1) || note "config link rerun failed: $out"
  [[ "$out" == *"0 linked, 3 already linked, 0 backed up, 0 refused."* ]] || note "rerun changed something: $out"
  echo "PASS: rerunning config link is a no-op"
}

test_unregistered_name_refused() {
  mkdir -p "$HOME/stray-cfg/claude"
  echo x >"$HOME/stray-cfg/claude/stray.md"
  if "$CODVPS_BIN" config link stray-cfg >/dev/null 2>&1; then
    note "config link accepted an unregistered directory"
  fi
  [[ ! -L "$HOME/.claude/stray.md" ]] || note "an unregistered directory was linked"
  rm -rf "$HOME/stray-cfg"
  echo "PASS: config link refuses an unregistered name"
}

test_home_edit_is_repo_edit() {
  echo "edited on the host" >"$HOME/.claude/CLAUDE.md"
  git -C "$CHECKOUT" status --porcelain | grep -q "claude/CLAUDE.md" || note "home edit did not show in git status"
  git -C "$CHECKOUT" checkout -q -- claude/CLAUDE.md
  echo "PASS: editing a linked home file edits the checkout"
}

test_credentials_refused() {
  echo '{}' >"$CHECKOUT/claude/.credentials.json"
  local out rc=0
  out=$("$CODVPS_BIN" config link agentcfg 2>&1) || rc=$?
  [[ $rc -ne 0 ]] || note "config link succeeded with a credential in the repo"
  [[ "$out" == *"refused ~/agentcfg/claude/.credentials.json"* ]] || note "credential refusal not reported: $out"
  [[ ! -L "$HOME/.claude/.credentials.json" ]] || note "a repo credential was linked"
  rm -f "$CHECKOUT/claude/.credentials.json"
  echo "PASS: credentials in the config repo are refused"
}

test_doctor_warns_on_drift() {
  rm "$HOME/.codex/AGENTS.md"
  echo "rewritten by a tool" >"$HOME/.codex/AGENTS.md"
  local out
  out=$("$CODVPS_BIN" doctor 2>&1 || true)
  # shellcheck disable=SC2088
  [[ "$out" == *"WARN: $HOME/.codex/AGENTS.md is no longer a link"* ]] || note "doctor did not warn about the replaced link: $out"
  rm "$HOME/.codex/AGENTS.md"
  "$CODVPS_BIN" config link agentcfg >/dev/null 2>&1 || note "relink after drift failed"
  echo "PASS: doctor warns when a linked file was replaced"
}

test_unlink() {
  local out
  out=$("$CODVPS_BIN" config unlink 2>&1) || note "config unlink failed: $out"
  [[ "$out" == *"Replaced 3 link(s) with regular copies"* ]] || note "unexpected unlink output: $out"
  for p in .claude/CLAUDE.md .claude/skills/demo/SKILL.md .codex/AGENTS.md; do
    [[ -f "$HOME/$p" && ! -L "$HOME/$p" ]] || note "$p is not a regular file after unlink"
  done
  [[ "$(cat "$HOME/.claude/CLAUDE.md")" == "repo rules" ]] || note "unlink did not keep the repo content"
  [[ ! -e "$HOME/.config/codvps/config-repo" ]] || note "config repo still recorded after unlink"
  echo "PASS: config unlink replaces links with regular copies"
}

setup
test_link
test_rerun_is_noop
test_unregistered_name_refused
test_home_edit_is_repo_edit
test_credentials_refused
test_doctor_warns_on_drift
test_unlink

if [[ $FAILED -ne 0 ]]; then
  echo "FAIL: one or more config link assertions failed"
  exit 1
fi
echo "PASS: codvps config link and unlink work end to end"
