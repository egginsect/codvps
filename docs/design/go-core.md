# Go Core Design

**Reference implementation:** the reference implementation at commit the reference implementation

**Date:** 2026-09-27

## Executive Summary

codvps is a Go port of the reference implementation, a VPS lifecycle manager for Remote Control heads (Claude and Codex). The Go implementation replaces the reference's 5348-line Bash CLI while preserving exact command parity, test behavior, and system integration. This document specifies the port scope, architecture, and ownership matrix for implementation work items.

---

## Command-Parity Inventory

Every reference command maps to one owner issue. Commands are ordered by dispatch hierarchy.

| Command | Flags | Output | Exit Codes | Files R/W | systemd Units | Issue |
|---------|-------|--------|-----------|-----------|---------------|-------|
| `codvps help` | none | Help text (literal) | 0 | none | none | |
| `codvps login claude` | none | Prompts, delegates to `claude auth login` | 0, 1 | writes `~/.claude/.credentials.json` (by claude CLI) | none | |
| `codvps login codex` | none | Refuses unless codex is a selected component; runs `codex login --device-auth` on the terminal with `CODEX_HOME=~/.codex-remote`; fails if no non-empty `auth.json` results | 0, 1 | writes `~/.codex-remote/auth.json` | none | |
| `codvps login github` | (see per-flag table below) | Preflight, auth, SSH key management, known_hosts, git config guidance, summary table | 0, 1, 2 | reads/writes `~/.ssh/` dirs, `~/.config/gh/`, `~/.gitconfig` | none | |
| `codvps repo add <url>` | none | Clone/reattach, attach to all enabled heads, seed workspace trust | 0, 1 | reads `~/.config/codvps/repositories`, `~/.claude.json`; writes both plus git state | `claude-remote@<name>.service`, `codex-remote@<user>.service` (restart) | |
| `codvps repo list` | none | Table: repository name, path, Claude head per-repo state (the Codex head is host-wide and appears only in the `Heads:` line) | 0 | reads registry, Claude state files | none | |
| `codvps repo remove <name>` | none | Deregister, detach from all heads, summary | 0, 1 | writes registry, stops and manages systemd units | `claude-remote@<name>.service`, `codex-remote@<user>.service` | |
| `codvps head enable <claude\|codex>` | none | Enable head, attach all registered repos, restart units | 0, 1 | reads registry, writes state files, manages systemd --user units, system Codex unit | `claude-remote@.service` (all), `claude-remote.slice`, `codex-remote@<user>.service` | |
| `codvps head disable <claude\|codex>` | none | Disable head, stop units, confirm | 0, 1 | writes state, stops units | `claude-remote@.service` (all), `codex-remote@<user>.service` | |
| `codvps head list` | none | Table: host head state, per-repo Claude head enable/disable | 0 | reads all state files | none | |
| `codvps head pair codex` | none | JSON with short-lived pairing code, then the session-hook approval note; only for the managed head (unit enabled and active, daemon reachable) | 0, 1 | none (delegates to `codex remote-control pair --json`) | none | |
| `codvps heritage sync` | none | Clone/update heritage, run install.sh, register repo | 0, 1 | reads `DEV_HERITAGE_URL` env, clones/updates, delegates to install.sh | depends on install.sh | not ported (legacy) |
| `codvps update [provider] [--yes]` | `--yes` restarts without asking | Run each selected, installed CLI's vendor updater; print `<name>: <old> -> <new>` or `<name>: <v> is current`; restart the active heads whose CLI changed after `--yes` or a typed `yes` (no terminal: print the units and "rerun with --yes", never prompt) | 0, 1 | runs vendor updaters as the operator; `systemctl [--user] restart` | none ||
| `codvps config link <repo-url\|name>` | none | Register the config repo like `repo add`, then symlink each file under its `claude/`, `codex/`, `agents/`, `opencode/`, `cursor/` dirs into the matching home (`codex/` into both `~/.codex` and `~/.codex-remote`); a differing home file is moved to `~/.config/codvps/config-link-backup/`; credentials and state dirs are refused. Git in the checkout is the history | 0, 1 | writes `~/.config/codvps/config-repo` and home symlinks | none ||
| `codvps config unlink` (hidden) | none | Replace every link with a regular copy and forget the config repo | 0, 1 | removes `~/.config/codvps/config-repo` | none ||
| `codvps components list` | none | Table: selected CLIs and switch (claude, codex, cursor, opencode, none) | 0 | reads `/usr/local/libexec/codvps/codvps-components` | none ||
| `codvps components status` | none | Per-component computed state (available, installed, selected, version info) | 0 | probes available commands, reads registry state | none ||
| `codvps status` | none | Host head state, per-repo head state, Codex daemon status, runtime versions | 0 | reads all state files, probes systemd and Codex daemon | none ||
| `codvps doctor` | none | Prerequisite and health checks (tools, permissions, state, Codex control-socket probe) | 0 (no FAIL), 1 (one or more FAIL), 2 (could not run the checks) | reads state files, probes systemd, checks paths | none ||
| `codvps install` | `--components <list>`, `--switch <none>` | Selection, progress, warnings, next steps; run as root through sudo | 0, 1 | writes `/etc/codvps/components.json`, `/usr/local/bin/codvps`, the embedded unit files, `/usr/local/bin/{uv,uvx,codex}` links; as the operator: `~/.config/codvps/`, `~/.bashrc` PATH line (and removes the retired sandbox's membership manifest and dangling control-socket link) | installs `claude-remote@.service`, `claude-remote.slice`, `codex-remote@.service`, `codex-remote-watchdog.{service,timer}`; retires the legacy `--user` `codex-remote.service` ||
| `codvps uninstall` | `--dry-run` | Per-path removed/skipped/already-absent report, then what was left in place | 0, 1 | removes the fixed set of artifacts the provider definitions and install declare | stops/disables `claude-remote@*`, the watchdog timer and `codex-remote@<operator>` ||
| `codvps help` / `codvps -h` / `codvps --help` | none | Print usage and command list | 0 | none | none ||
| `codvps version` | none | Print CLI version (injected via ldflags) | 0 | none | none ||
| `codvps internal codex-isolation validate\|sync <operator>` | removed | Owner decisionremoved codvps's own sandbox around Codex Remote; the command, the `ExecStartPre=+` step and the `workspaces.conf` drop-in it wrote no longer exist. Install removes a stale generated `workspaces.conf` | n/a | n/a | n/a ||
| `codvps internal shell-exec [--provider NAME] [--unset VAR]... -- CMD [ARGS...]` | as shown (internal; the `ExecStart` wrapper of every head unit: Claude, Codex, Cursor and OpenCode) | Run the operator's login interactive shell (`$SHELL`, else the passwd shell, else `/bin/sh`; stdin `/dev/null`, 10s timeout) to print a marker and `env -0`, parse only what follows the marker, remove the `--unset` variables and the named provider's `Env.Remove` variables (after the shell, so an rc file cannot re-add them), then replace the process with CMD (`syscall.Exec`, CMD resolved on the captured PATH, args passed as a slice). If the shell fails, times out or prints no marker: one stderr line, then CMD starts with the unit environment minus the same removals | CMD's own; 1 for an unknown provider or a CMD that cannot be found | none | none ||
| `codvps head ensure codex` | none (internal; invoked by watchdog timer) | Fast idempotent health check; managed restart if the daemon's socket is missing and the head is enabled; backs off after 3 failures in 10 minutes | 0, 1 | connects to the app-server control socket; reads/writes `~/.local/state/codvps/codex-watchdog.failures` | `codex-remote@<user>.service` (restart if needed) ||

### `codvps login github` — one row per flag

Derived from the `login_github` argument parser and driven end-to-end by `tests/login-github.sh` (see `docs/test-parity.md`).

| Flag | Behavior | Exit Codes | Files R/W |
|---|---|---|---|
| (no flags — interactive default) | Runs the preflight check, installs or upgrades gh on demand when it is missing or too old for `gh auth status --json` (needs sudo, or `sudo codvps install`), then all seven idempotent steps (auth, key, key registration, known_hosts, ssh config, git identity, verify) with prompts wherever a choice is needed. Without a terminal the device flow still starts (its code and URL are printed) and each prompt takes its default answer, reported as `assuming yes: ...`; an empty key passphrase is never assumed (needs `--passphrase-empty`) | 0, 1, 2 | `~/.ssh/*`, `~/.config/gh/`, `~/.gitconfig` |
| `--non-interactive` | Same seven steps, but any step that would otherwise prompt instead reports `pending` (or exits 2 for auth) rather than blocking on input; unlike a missing terminal, it never starts the device flow | 0, 1, 2 | same |
| `--key skip` | Skips SSH key management for this run; later steps proceed with no selected key | 0 | none for the key step |
| `--key generate` | Generates a new key at `~/.ssh/codvps_github_ed25519`; non-interactively this requires `--passphrase-empty` or the step fails | 0, 1 | writes `~/.ssh/codvps_github_ed25519{,.pub}` at mode 0600 |
| `--key existing:/path/to/key` | Uses the key at the given path instead of generating one | 0, 1 | reads the given path |
| `--passphrase-empty` | Acknowledges that a non-interactively generated key will have an empty passphrase; required together with `--key generate` outside a tty | 0, 1 | none |
| `--identity-name <name>` | Sets git `user.name` non-interactively; never overrides an already-configured identity | 0 | writes `~/.gitconfig` only if `user.name` was unset |
| `--identity-email <email>` | Sets git `user.email` non-interactively; never overrides an already-configured identity | 0 | writes `~/.gitconfig` only if `user.email` was unset |
| `--yes` | Confirms an SSH-key-registration preview that would otherwise stay `pending` without a terminal, and answers every prompt with its default even on a terminal (each reported as `assuming yes: ...`) | 0 | `gh ssh-key add` (registers the key with GitHub) |
| `--defer` | Preflight check only; prints status and returns 0 without making any change | 0 | none |
| `--help` | Prints usage and the flag list, then exits 0 | 0 | none |

### `codvps login github` — one row per summary/outcome state

`github_print_summary` tracks one status per check (`gh_account`, `credential_storage`, `identity`, `key`, `key_registration`, `known_hosts`, `ssh_auth`) and then derives one overall outcome.

| State | Meaning | Reachable how |
|---|---|---|
| `ok` | The check succeeded / the step is complete | Default success path for any of the seven checks |
| `pending` | The step needs an operator action that non-interactive mode won't take on its own | e.g. no `--key` choice given non-interactively; registration attempted without `--yes`; git identity with nothing existing and no `--identity-*` flags |
| `skipped` | Default status for a check the run never reached or touched | Any check left unset in the status map when the summary prints |
| `failed` | The check failed outright | Reachable in principle, but every failure path in the reference calls `die()` *before* the summary prints, so this state is never actually observed in practice today — the Go port should preserve that fail-fast behavior rather than relying on the summary table to surface failures |
| overall: "GitHub setup complete." | No check is `pending` and none is `failed` | exit 0 |
| overall: "GitHub setup incomplete... rerun, or --defer to recheck" | At least one check is `pending`, none is `failed` | exit 0 (a pending non-interactive step is a valid outcome, not an error) |
| overall: "GitHub setup FAILED" | At least one check is `failed` | exit 1 |

### `codvps update [provider] [--yes]` — one row per provider value and flag

> **Superseded by.** codvps no longer pins or copies CLIs: each coding CLI is installed by its vendor installer on first `codvps enable`/`login`, `codvps update [provider] [--yes]` runs the vendor updaters and restarts only the active heads whose CLI changed, and install removes what this layer left (`internal/install/legacy.go`). Kept below for history.

`RUNTIME_PROVIDERS=(claude codex)`; `runtime_resolve_providers` resolves the request.

| Value / Flag | Behavior | Exit Codes | systemd Units |
|---|---|---|---|
| (no provider given) | Resolves to every currently *selected* managed provider (component-gated; an unselected provider is silently excluded, not an error) | 0, 1 | `codvps-updater@<user>.service`, `codvps-runtime-publish@.service` for each resolved provider |
| `all` (explicit) | Same resolution as the no-provider default | 0, 1 | same |
| `claude` | Check/stage/apply the Claude runtime only; refuses with a component-selection error if claude is not selected | 0, 1 | `codvps-runtime-publish@<user>.service` |
| `codex` | Check/stage/apply the Codex runtime only; refuses if codex is not selected | 0, 1 | `codvps-runtime-publish@<user>.service` |
| unrecognized provider word | Dies with "unknown runtime provider: `<word>`" | 1 | none |
| `--yes` | Skips the typed `yes` confirmation before the apply phase pins and restarts the active heads (any other answer dies with "update apply cancelled") | 0, 1 | same as the resolved provider(s) |
| `update help` / `update --help` | Prints update usage and exits 0 | 0 | none |
| old verbs `check`, `stage`, `status`, `apply` | Removed; dies with "removed: use codvps update [provider]" | 1 | none — **not ported: removed verb (owner rule)** |

One run prints the runtime table after the check and after staging, then the plan; the final table follows a successful apply. A provider whose check failed in this run stages nothing (an update never acts on a release it could not confirm), and apply activates only what this run staged and the publisher confirmed. After pinning it restarts only the heads that were active, verifies each (resolved `ExecStart`/`CODVPS_CODEX_BIN`, the release's `--version`, and the main process's executable), and on any failure restores the previous selection and pin and restarts those heads again.

**Removed commands:** `heritage sync` is not ported; it becomes an operator task outside codvps (link a config repo with `codvps config link <repo-url>`, or run a one-off script). `models` (refresh, plan, apply, doctor, restore, ui, canary) is removed. The old `update` verbs `check`/`stage`/`status`/`apply` and the standalone `restart` command are likewise not ported (see the reconciliation table below).

### Case-arm reconciliation (exact)

The reference implementation's entry point dispatches through a top-level `case "$1"` in `main()` (around L5161-5345) plus nested `case "$2"` / if-elif dispatchers for each command group. Counted by reading that block directly: **50 case arms total** (14 top-level + 36 nested sub-arms). Every arm is mapped below to an inventory row above, to a per-provider/per-flag row in the tables just above, to "not ported" with a reason, or to "structural" (the arm only prints a usage/`die` message inherent to any CLI dispatcher, not a portable feature in its own right — the Go dispatcher must still reproduce the same usage text and exit code, but it isn't tracked as a separate command row).

| # | Case arm (reference implementation line) | Maps to |
|---|---|---|
| 1 | `case "$1"`: `login` (L5162) | Dispatches to the `login` sub-arms below (not a command itself) |
| 2 | `case "$1"`: `heritage` (L5177) | **Not ported: `heritage sync` is legacy** (owner rule) |
| 3 | `case "$1"`: `repo` (L5181) | Dispatches to the `repo` sub-arms below |
| 4 | `case "$1"`: `head` (L5204) | Dispatches to the `head` sub-arms below |
| 5 | `case "$1"`: `update` (L5228) | Dispatches to the `update` branches below |
| 6 | `case "$1"`: `config` (L5244) | Dispatches to the `config` sub-arms below |
| 7 | `case "$1"`: `runtime` (L5277) | Dispatches to the `runtime` sub-arms below |
| 8 | `case "$1"`: `restart` (L5291) | **Not ported: removed verb** (owner rule); reference `die()`s pointing at `models refresh`, since removed |
| 9 | `case "$1"`: `models` (L5294) | Dispatches to the `models` sub-arms below |
| 10 | `case "$1"`: `components` (L5309) | Dispatches to the `components` sub-arms below |
| 11 | `case "$1"`: `status` (L5329) | Row: `codvps status` |
| 12 | `case "$1"`: `doctor` (L5333) | Row: `codvps doctor` |
| 13 | `case "$1"`: `help \| -h \| --help` (L5337) | Row: `codvps help` / `codvps -h` / `codvps --help` |
| 14 | `case "$1"`: `*` unknown (L5341) | Structural: prints usage to stderr, `die "unknown command: $1"` |
| 15 | `login` → `case "$2"`: `claude` (L5165) | Row: `codvps login claude` |
| 16 | `login` → `case "$2"`: `codex` (L5169) | Row: `codvps login codex` |
| 17 | `login` → `case "$2"`: `github` (L5173) | Rows: `codvps login github` per-flag / per-outcome-state tables above |
| 18 | `login` → `case "$2"`: `*` unknown (L5174) | Structural: `die "unknown login target: $2"` |
| 19 | `repo` → `case "$2"`: `help \| --help` (L5184) | Structural: `repo_usage()` text |
| 20 | `repo` → `case "$2"`: `add` (L5188) | Row: `codvps repo add <url>` |
| 21 | `repo` → `case "$2"`: `list` (L5191) | Row: `codvps repo list` |
| 22 | `repo` → `case "$2"`: `remove` (L5195) | Row: `codvps repo remove <name>` |
| 23 | `repo` → `case "$2"`: `*` unknown (L5199) | Structural: `die "unknown repo command: $2"` |
| 24 | `head` → `case "$2"`: `enable \| disable` (L5207) | Rows: `codvps head enable <claude\|codex>`, `codvps head disable <claude\|codex>` |
| 25 | `head` → `case "$2"`: `pair` (L5210) | Row: `codvps head pair codex` |
| 26 | `head` → `case "$2"`: `list` (L5214) | Row: `codvps head list` |
| 27 | `head` → `case "$2"`: `ensure` (L5218) | Row: `codvps head ensure codex` (internal) |
| 28 | `head` → `case "$2"`: `*` unknown (L5223) | Structural: `die "unknown head command: $2"` |
| 29 | `update`: no args (L5231-5233) | Row: `codvps update` provider table, "(no provider given)" |
| 30 | `update`: `help \| --help` (L5234-5235) | Row: `codvps update` provider table, `update help`/`--help` |
| 31 | `update`: old verb `check\|stage\|status\|apply` (L5236-5238) | **Not ported: removed verb** (owner rule) |
| 32 | `update`: else — provider word (+ `--yes`) (L5239-5241) | Rows: `codvps update` provider table, `claude`/`codex`/`all`/unrecognized + `--yes` |
| 33 | `config` → `case "$2"`: `help \| --help` (L5247) | Structural: `config_usage()` text |
| 34 | `config` → `case "$2"`: `connect` (L5251) | Superseded by `codvps config link` |
| 35 | `config` → `case "$2"`: `status` (L5254) | Superseded by `codvps config link` |
| 36 | `config` → `case "$2"`: `diff` (L5258) | Superseded by `codvps config link` |
| 37 | `config` → `case "$2"`: `sync` (L5262) | Superseded by `codvps config link` |
| 38 | `config` → `case "$2"`: `restore` (L5265) | Superseded by `codvps config link` |
| 39 | `config` → `case "$2"`: `disconnect` (L5268) | Superseded by `codvps config link` |
| 40 | `config` → `case "$2"`: `*` unknown (L5272) | Structural: `die "unknown config command: $2"` |
| 41 | `runtime` → `case "$2"`: `path` (L5280) | Removed (no pinned runtimes) |
| 42 | `runtime` → `case "$2"`: `exec` (L5283) | Removed (no pinned runtimes) |
| 43 | `runtime` → `case "$2"`: `*` unknown (L5286) | Structural: `die "unknown runtime command: $2"` |
| 44 | `models` → `case "$2"`: `refresh` (L5297) | **Not ported: removed** |
| 45 | `models` → `case "$2"`: `plan\|apply\|doctor\|restore\|ui\|canary` (L5301) | **Not ported: removed** |
| 46 | `models` → `case "$2"`: `*` unknown (L5304) | Structural: `die "unknown models command: $2"` |
| 47 | `components` → `case "$2"`: `help \| --help` (L5312) | Structural: `components_usage()` text |
| 48 | `components` → `case "$2"`: `list` (L5316) | Row: `codvps components list` |
| 49 | `components` → `case "$2"`: `status` (L5320) | Row: `codvps components status` |
| 50 | `components` → `case "$2"`: `*` unknown (L5324) | Structural: `die "unknown components command: $2"` |

**Tally:** 50 arms total — 8 are parent arms that only dispatch to the sub-arms listed under them (rows 1, 3-7, 9-10); 28 map to a concrete inventory or per-flag/per-provider row; 4 are explicitly **not ported** per the owner rules (`heritage sync`, the standalone `restart` verb, the old `update` verbs, and the removed `models` verbs); 10 are **structural** dispatcher behavior (usage text or an "unknown X" `die`) that the Go dispatcher must still replicate byte-for-byte in its default/help-handling code, but which isn't a separately tracked feature. Zero arms are unaccounted for.

---

## Installation and Bootstrap

Installation is split between the binary and a bootstrap script.

**`codvps install` (in the binary, run as `sudo codvps install`):** everything that only wires up what is already on disk, unit-tested and idempotent:
1. **Preflight (no host mutation):** require euid 0 and a non-root `SUDO_USER` operator; parse `--components`/`--switch`; refuse a fresh, flagless, TTY-less host; resolve the selection (flags, then the existing registry, then detection on a host codvps installed before, then an interactive prompt). install is the single manager of its unit files, binary and links: nothing is refused for already existing, and each existing file that differs from what install writes is named (`replacing <path>: ...`) before it is overwritten.
2. **Registry:** write `/etc/codvps/components.json` (root, 0644) and report deselections (nothing is removed).
3. **Binary and operator state:** place the running binary at `/usr/local/bin/codvps`, then run `codvps internal operator-state` as the operator (runuser, scrubbed environment) to create `~/.config/codvps` (0700), the `~/.bashrc` PATH line and, and, with codex selected, removes what the retired Codex sandbox left in the operator home (the membership manifest, and a control-socket link dangling into the old unit's private `/tmp`); root then verifies uid:gid:mode.
4. **systemd:** enable linger and start `user@<uid>.service`; record whether a Codex head is enabled (system unit or the legacy `--user` unit), stop both and retire the legacy unit; remove a stale generated `codex-remote@<operator>.service.d/workspaces.conf` (exact path and content marker; a drop-in the operator wrote stays) and its empty directory; write the embedded units; reload both managers; enable the watchdog for an enabled head; re-enable the Codex head (started when a Codex login exists).
5. **Tooling:** link `~/.local/bin/{uv,uvx}` and each selected provider's declared links (codex) into `/usr/local/bin` (the unit PATH), replacing and naming whatever else is at those paths.

**Bootstrap installer (`install.sh`):** small and host-agnostic; it never changes the host itself. It detects the platform (linux amd64/arm64, anything else refused), resolves the release selector once to an exact tag (`--version` is exact and never falls back to the latest release; `latest` follows the releases page's `/latest` redirect), downloads that tag's binary and `SHA256SUMS` over HTTPS, requires exactly one well-formed checksum entry that matches and a binary that reports the same version, and fails before keeping anything otherwise. The verified binary is kept at `~/.local/share/codvps/<tag>/codvps`; the script shows what `codvps install` will change, then hands off to `sudo <that binary> install` with `--components`/`--switch` forwarded. Under `curl | bash` stdin is the script, so the confirmation and the handoff read the terminal from `/dev/tty`; with no terminal it prints the exact next command instead, so a non-interactive run never hangs. All code is in functions with `main` on the last line, so a truncated download runs nothing. Tests: `test/install/bootstrap_test.sh` (every privileged and network command stubbed, a local release fixture).

**Host provisioning (in `codvps install`, `internal/install/provision.go`):** the network-dependent half of the reference installer, run as part of `sudo codvps install` after every refusal and before codvps writes anything of its own. The whole plan is printed first (each step with "will run" or why it is skipped), then each command is printed before it runs through the injected runner. Root steps: apt packages (git, openssh-client, curl, ca-certificates, jq, ripgrep, bubblewrap, apparmor, iproute2); the bubblewrap AppArmor profile at `/etc/apparmor.d/codvps-bwrap-userns-restrict`, extracted from `apparmor-profiles` unless the distro ships `bwrap-userns-restrict`, and activated when AppArmor is enabled; gh from GitHub's apt repository; Node.js from NodeSource (so the units find `/usr/bin/node`) and pnpm through corepack; the Claude CLI (npm) when claude is selected; a 4G `/swapfile` in `/etc/fstab`; ufw allowing every sshd port before denying other incoming traffic. Operator steps, once `~/.codex-remote` is the verified 0700 directory: the Codex standalone installer with `CODEX_HOME=~/.codex-remote` when codex is selected, and uv. A step whose result is already present is skipped (a rerun fetches nothing again and never upgrades a CLI under live heads; that is the managed runtimes',), swap, ufw and uv failures are warnings as in the reference, swap/ufw/AppArmor activation are skipped in a container, a file provisioning writes is rewritten (and named) when it differs, and an existing `/swapfile` that is not active swap stops provisioning rather than being reformatted. `--skip-provision` skips all of it.

**`codvps uninstall [--dry-run]`:** stop and disable every `claude-remote@*` instance, the watchdog timer and `codex-remote@<operator>`; remove the fixed declared set: every provider's unit files, the stale generated `workspaces.conf` drop-in and the runtime pins, the runtime tree and `/run/codvps`, the binary, and the `/usr/local/bin` links -- a link path holding anything but codvps's own symlink is reported and kept, and so is a directory where a file belongs; reload both managers. `--dry-run` prints the same set without changing anything. Repositories, codvps state, credentials, the component registry and linger are kept, so `sudo codvps install` restores the host.

> **Superseded by.** codvps no longer pins or copies CLIs: each coding CLI is installed by its vendor installer on first `codvps enable`/`login`, `codvps update [provider] [--yes]` runs the vendor updaters and restarts only the active heads whose CLI changed, and install removes what this layer left (`internal/install/legacy.go`). Kept below for history.

**Managed runtimes at install:** install also writes the updater/publisher units and the tmpfiles.d snippet, creates the `/run/codvps` drop boxes, seeds the installed `claude` and standalone `codex` as each provider's first root-owned release, records each selection in the operator's state (as the operator, through `codvps internal runtime-state`; a recorded selection whose release no longer exists is replaced by the seeded one), registers the Codex session hook, pins every head, and only then enables the two request watchers so no watcher races install's own publish. Uninstall removes the pins, the runtime tree and the drop boxes but keeps the selection state.

**Components and switch matrix:**
The `install.sh --components <list> --switch <switch>` combination determines which coding CLIs are installed and which runtime selection mechanism is used:

| Components | Switch | Claude | Codex | Notes |
|---|---|---|---|---|
| claude | none | installed | not installed | Claude only; uses default system Claude |
| codex | none | not installed | installed | Codex only; uses default system Codex |
| claude,codex | none | installed | installed | Both CLIs; Claude and Codex on system path |
| none | none | neither installed | both skipped | Minimal install; no coding CLIs (testing/bootstrap only) |
| claude,codex | cc-switch | error: owns cc-switch | (not ported) | Reserved for future; return error with guidance |

**install.sh one-liner:**
```bash
curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh | bash [-s -- --version vX.Y.Z --components claude,codex --switch none]
```
Run it as the operator (a sudo-capable login account, not root). `install.sh` is published as a release asset and listed in that release's `SHA256SUMS`. With a terminal it asks before running `sudo codvps install`; without one it prints that command.

---

## systemd Contract

### User-Scoped Units (via systemd --user)

#### `claude-remote@<repo>.service`
- **Scope:** Per-repository, one instance per registered repository
- **Start condition:** `codvps head enable claude`
- **Executable:** `codvps internal shell-exec --provider claude -- claude remote-control --spawn worktree --capacity 4 --no-create-session-in-dir --remote-control-session-name-prefix cloud-<repo>` (the operator's login-shell environment; see `codvps internal shell-exec` above)
- **Restart:** `Restart=always`, `RestartSec=5`
- **Working directory:** `~/<repo>` (repository root)
- **Budget:** Member of `claude-remote.slice` (see below)
- **Environment unset:** `ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_BASE_URL` (`UnsetEnvironment=` in the unit, and again by `shell-exec` from the provider's `Env.Remove`, after the login shell ran)
- **After:** `network-online.target`

#### `claude-remote.slice`
- **Scope:** Host-wide, shared budget for all Claude Remote instances
- **Memory limits:** `MemoryHigh=4G`, `MemoryMax=5G`
- **CPU quota:** `CPUQuota=300%` (3.0 cores equivalent)
- **Purpose:** Prevent any single repository or rogue worktree from consuming unlimited resources across all Claude heads

### System-Scoped Units (via systemd system, requires root via install.sh)

#### `codex-remote@<operator>.service`
- **Scope:** One instance per operator user on the VPS
- **Start condition:** `codvps head enable codex` (non-interactive, requires prior auth and repo registry)
- **Executable:** `codvps internal shell-exec --provider codex -- codvps internal codex-remote-start` (the operator's login-shell environment, as for every head; `codex-remote-start` itself runs `codex remote-control start --json` from the standalone install with the default `CODEX_HOME` (`~/.codex`), retrying once after Codex's transient errored-relay failure). `ExecStop` is Codex's own `remote-control stop --json`. Nothing else: since codvps adds no sandbox, so there is no `ExecStartPre`, `ProtectSystem`, `ProtectHome`, `PrivateTmp`, `NoNewPrivileges`, bind mount or `workspaces.conf` drop-in. The daemon is the ordinary Codex app-server daemon for `~/.codex`, shared with the operator's interactive `codex` CLI; Codex's own per-command sandbox (`sandbox_mode` in `config.toml`) is unchanged. The unit stays a system unit with `User=%i` (enable, disable, ensure and the watchdog already go through `sudo systemctl`; a `--user` unit would add nothing and need a migration)
- **Type:** `oneshot` with `RemainAfterExit=yes` (daemon is a separate process, not the main PID)
- **Disable:** `codvps disable codex` runs `codex remote-control stop --json`, the only off-switch the CLI has (`start`, `stop`, `pair`); it stops the app-server daemon, which the interactive CLI shares
- **State files read:** repository registry is not read; the head needs only `~/.codex/auth.json` and the standalone install
- **Restart:** Deliberately no `Restart=` (allows manual lifecycle control; model refresh can kill daemon independently)
- **Health probe:** `codvps head ensure codex` (watches via timer; the daemon is live when `~/.codex/app-server-control/app-server-control.sock` accepts a connection, as the codex CLI connects)

#### `codex-remote-watchdog.service` + `codex-remote-watchdog.timer`
- **Scope:** systemd --user units in `/etc/systemd/user`, enabled and disabled together with the Codex head; fires once per 60 seconds
- **Executable:** `codvps head ensure codex`
- **Purpose:** Restart Codex Remote (through `sudo systemctl reset-failed` + `restart`) if its daemon is unavailable and the head is enabled; after 3 failed attempts in 10 minutes it backs off and only logs
- **Boot delay:** `OnBootSec=10s`, then `OnUnitActiveSec=60s`
- **Health check:** Connects to the control socket

> **Superseded by.** codvps no longer pins or copies CLIs: each coding CLI is installed by its vendor installer on first `codvps enable`/`login`, `codvps update [provider] [--yes]` runs the vendor updaters and restarts only the active heads whose CLI changed, and install removes what this layer left (`internal/install/legacy.go`). Kept below for history.

#### `codvps-updater@<operator>.service` + `codvps-update-request@<operator>.path`
- **Scope:** One instance per operator (system units, `User=%i`), started by the `.path` unit whenever a file appears in `/run/codvps/update-requests` (the Codex session hook queues `check.session`)
- **Type:** `oneshot`
- **Executable:** `codvps internal update-worker drain` — a Go subcommand of the main `codvps` binary, not a separate script (see Root Helper Decision below)
- **Purpose:** Check vendor releases (throttled: once per `CODVPS_UPDATE_THROTTLE_MINUTES`, default 60; a shared `flock` on `~/.cache/codvps/update.lock` makes a concurrent run skip), consume the session hook's `check.session` request, and stage what a `stage.<provider>` request names. The drop box is root-owned 1733, which the operator can write but not list, so the drain removes each known request name (`check.session`, `stage.<provider>` for every managed runtime) instead of reading the directory. A vendor failure is recorded in the provider's state (check-and-notify), never a failed run
- **Security:** `NoNewPrivileges=yes`, empty `CapabilityBoundingSet=`, `ProtectSystem=strict` with write access only to `~/.config/codvps`, `~/.cache/codvps` and the two request drop boxes: it can download and verify, never publish, pin or restart

#### `codvps-runtime-publish@<operator>.service` + `codvps-runtime-publish@<operator>.path`
- **Scope:** One instance per operator, started by the `.path` unit whenever a `*.request` file appears in `/run/codvps/publish-requests`. The instance is the operator, not the provider: the one account whose staging tree the root publisher trusts is fixed by the enabled instance, never by whoever wrote into the world-writable drop box (requests not owned by that operator or root are ignored)
- **Type:** `oneshot`
- **Executable:** `codvps internal runtime-publish <operator>` — a Go subcommand of the main `codvps` binary, not a separate script (see Root Helper Decision below)
- **Purpose:** Copy a verified staged tree to `/opt/codvps/runtimes/<provider>/versions/<version>/` (root, privileged), re-verifying the executable's checksum from `RELEASE.json` on the staged tree and the assembled copy, then renaming into place. Every request is consumed before it is acted on and gets a recorded outcome, `/run/codvps/publish-results/<request-id>.json` (`{provider, version, ok, error}`), which `codvps update` waits for (bounded, 120 s) instead of polling `/opt` — a publish is known the moment it finishes, and a refused publish is reported with its reason instead of as a timeout
- **Security:** `PrivateNetwork=yes`, `NoNewPrivileges=yes`

Activation is neither unit's: `codvps update` pins through `sudo -n codvps internal runtime-pin <operator> [provider=version...]`, which resolves each selection to a verified root-owned executable (failing the whole run, drop-ins untouched, on any selection that exists but does not resolve), writes the `runtime.conf` drop-ins (Claude: an absolute versioned `ExecStart=` with `DISABLE_AUTOUPDATER=1`/`DISABLE_UPDATES=1`; Codex: `CODVPS_CODEX_BIN` plus a reset-and-redeclared `ExecStartPre=` isolation guard), rewriting whatever is at those paths, and reloads both managers.

---

## State and Config Compatibility Table

Every path codvps itself reads or writes is listed below, together with the shared third-party paths it touches and the systemd unit names it owns.

| Path | Type | Purpose | Notes |
|------|------|---------|-------|
| `~/.config/codvps/` | dir | codvps state root | User-owned config directory |
| `~/.config/codvps/repositories` | file | Repository registry (sorted names, one per line) | Shared by the per-repository heads |
| `~/.config/codvps/claude-head-enabled` | file | Claude head on/off flag (empty file = enabled) | Simple toggle state |
| `~/.config/codvps/config-mapping.json` | file | Locally-approved config mapping (source paths → target paths) | User approval needed before sync can use it |
| `~/.config/codvps/runtimes/` | dir (0700) | Per-provider runtime state, `<provider>.state` (0600): `available`, `staged`, `selected`, `selected_path`, `previous`, `staging_support`, `last_error`, `checked_at`, `channel` | Parsed as key=value, never sourced; kept by uninstall |
| `~/.local/share/codvps/config-source/` | dir | Cached config source clone (for git URL sources) | Transient; can be regenerated on next sync |
| `~/.local/state/codvps/config/` | dir | Config sync state (applied.json, receipts dir tree) | Audit trail of applied changes and rollback data |
| `~/.cache/codvps/update.lock` | file | Update worker lock file | Prevents concurrent update checks |
| `/opt/codvps/runtimes/` | dir (root) | Verified, published runtime versions (claude, codex) | Root-owned, world-traversable; removed by uninstall |
| `/opt/codvps/runtimes/<provider>/versions/<version>/` | dir (root) | One published release: `bin/<claude\|codex>` and its `RELEASE.json` evidence (vendor and binary checksums, verification) | No mutable `current` link: the pin drop-ins name the versioned path, so a staged release is never adopted by a crash-restart |
| `/run/codvps/update-requests/` | dir (ephemeral, 1733) | Request markers for the updater (`check.*`, `stage.<provider>`) | Consumed by each drain; recreated every boot by `/etc/tmpfiles.d/codvps-runtime.conf` |
| `/run/codvps/publish-requests/` | dir (ephemeral, 1733) | Publish requests (`<provider>.<id>.request`) for the root publisher | Consumed before each is acted on |
| `/run/codvps/publish-results/` | dir (ephemeral, 0755) | The publisher's recorded outcome per request (`<id>.json`) | Root-written; pruned after an hour |
| `~/.cache/codvps/staging/<provider>/<version>/` | dir | A downloaded, verified release waiting to be published | Operator-owned |
| `/etc/systemd/{user,system}/…/runtime.conf` | file (root) | The pin drop-ins for `claude-remote@.service` and `codex-remote@<user>.service` | Rewritten by every pin; removed by uninstall |
| `/usr/local/libexec/codvps/` | dir (root) | Root-owned support directory: the `codvps-components` selection registry plus the fixed entry points systemd units invoke | Installed by install.sh; per the Root Helper Decision (below) the isolation-validate/update-worker/runtime-publish entry points are Go subcommands of the main `codvps` binary — no bash wrapper scripts remain here |
| `/etc/systemd/user/` | dir (root) | User-scoped systemd units | codvps owns `claude-remote@.service`, `claude-remote.slice`, `codex-remote-watchdog.service`/`.timer` |
| `/etc/systemd/system/` | dir (root) | System-scoped units | codvps owns `codex-remote@.service` (plus its generated `codex-remote@<user>.service.d/{workspaces,runtime}.conf`), `codvps-updater@.service`, `codvps-update-request@.path`, `codvps-runtime-publish@.service`, `codvps-runtime-publish@.path` |
| `~/.codex-remote/` | dir | Codex Remote state (auth, config, sessions, history) | Third-party (Codex CLI); shared, not owned by codvps |
| `~/.claude.json` | file | Claude CLI config (workspace trust, session history) | Third-party (Claude CLI); codvps only reads/writes `projects.<path>.hasTrustDialogAccepted` |
| `~/.ssh/` | dir | SSH keys and known_hosts | User-managed; codvps writes as user (not root) during `login github` |
| `~/.config/gh/` | dir | GitHub CLI config (auth tokens, hosts) | User-managed; third-party (`gh` CLI) |

---

## Pre-existing installations

codvps keeps no ownership record. `codvps install` is the single manager of the paths it declares -- its unit files, generated drop-ins, `/usr/local/bin/codvps` and the `/usr/local/bin` tooling links -- and overwrites them, naming each existing file it replaces. It still refuses what would make a privileged step unsafe: running as bare root, a symlinked legacy `~/codex-workspaces` or `~/.codex-remote`, and (on a fresh host with no selection and no terminal) any change at all. `codvps uninstall` removes the same fixed, declared set and nothing else; repositories, credentials, codvps state, the component registry and linger are kept.

Migrating a pre-existing installation made by some other tool is out of scope for the public tool: install takes over the declared paths, so an operator who needs a conflicting unit or link kept must move it first, or wait for namespaced installs (below).

## Providers: one definition per coding CLI

Each coding CLI and switch is described once, in `internal/providers`: a `Provider` with a name, kind (coding CLI or switch), capability row, executable, label, stored credential and optional facets -- `Provision` (host provisioning step), `Install` (unit files, generated drop-ins, `/usr/local/bin` links, preflight, detection, operator state, head retirement and restore, runtime seed, uninstall stop and kept paths), `Runtime` (a `runtimes.Spec`: release source, channel, pin drop-in, restart targets, vetted release), `Worker` (`runtime path|exec <name>`), `Login` (`login <name>`), `Head` (enable, disable, list, status rows, pair, ensure, and the `repo add|list|remove` hooks: attach, revoke/detach/resync, list column), `Doctor` (a `doctor.Suite`) and `Internal` (the `codvps internal` helpers the provider's units run). A facet a provider lacks is nil. `providers.All()` is the production set in report order; `Set.Validate()` checks that every capability a provider claims is backed by its facet, and the registry test runs it.

Every command iterates the set: install and uninstall (units, drop-ins, links, preflights, provisioning, seeding), `components list|status` and selection validation, `login`, `head enable|disable|list|ensure|pair`, `repo add|list|remove`, `status`, `doctor`, `update` and the runtime helpers (the specs come from `Set.Runtimes()`), `runtime path|exec`, and the top-level help rows. The per-CLI mechanics stay in their domain packages as named constructors that take the name and executable from the definition (`doctor.ClaudeSuite`, `runtimes.CodexRuntime`, `head.CodexStandalone`, ...), so no package outside `internal/providers` branches on a CLI's name.

To add a coding CLI:
1. Write `internal/providers/<cli>.go` returning a `*Provider`, and add it to `All()` in report order.
2. Give it the facets its capabilities claim: at least `Binary`, `Label`, an `Install` with its unit templates (add them to `internal/systemd`), and -- for `auth=yes` -- a `Credential` and `Login`; for `update=yes` a `Runtime` built from a `runtimes` constructor (or a new `runtimes.Source`/`Pin` pair); for `native_remote=yes` a `Head` whose `Host` implements `head.Host` and whose `Repo` returns a `repo.Head`; and a `Doctor` suite.
3. Run `go test ./internal/providers/` (`TestAllIsValid`), and extend the fake-provider tests (`internal/install`, `internal/cli`, `internal/components`) only if the new CLI needs a facet they do not exercise yet.

## Test-namespace override (planned)

A planned environment variable, `CODVPS_NAMESPACE` (default `codvps`), will prefix codvps's own state paths — e.g. `~/.config/$CODVPS_NAMESPACE/`, `~/.local/state/$CODVPS_NAMESPACE/`, `~/.local/share/$CODVPS_NAMESPACE/`, `~/.cache/$CODVPS_NAMESPACE/`, `/opt/$CODVPS_NAMESPACE/`, `/run/$CODVPS_NAMESPACE/`, `/usr/local/libexec/$CODVPS_NAMESPACE/` — and the unit names codvps defines outright, such as `$CODVPS_NAMESPACE-updater@<user>.service` and `$CODVPS_NAMESPACE-runtime-publish@<provider>.service`. The goal is that a side-by-side test install, run under a non-default namespace, cannot read, write, enable, or stop anything belonging to another installation.

The two systemd unit templates codvps does not itself name — `claude-remote@` and `codex-remote@`, whose base names are fixed by convention with the Claude and Codex CLIs — stay unprefixed under the default namespace (`codvps`), matching a production instance's unit names exactly. Under any non-default namespace, these also get the namespace prefix (e.g. `test-claude-remote@<repo>.service`, `test-codex-remote@<operator>.service`), so a test install's per-repository and per-operator units cannot collide with, or be started/stopped alongside, a production install's units.

The state paths already follow `CODVPS_NAMESPACE` (`internal/paths`), but unit names are not prefixed yet, so `codvps install` and `codvps uninstall` refuse any namespace other than `codvps` rather than write or stop the production units. Note that `sudo` resets the environment, so a namespace reaches a root command only when passed explicitly; the root subcommands take it from the environment today.

---

## Root Helper Decision

**Removed by owner decision ("let's not do sandbox by ourself").** The
earlier decision implemented a root-side `codvps internal codex-isolation
validate|sync` subcommand behind an `ExecStartPre=+` step to build a mount
namespace for Codex Remote. codvps no longer wraps Codex Remote in its own
sandbox, so there are no root-scoped subcommands: install (run as root through
sudo) and the units' fixed `ExecStart`/`ExecStop` are the only privileged
paths, and user commands remain unprivileged. The reason for the change: the
unit's `PrivateTmp` made the daemon write its control socket as a symlink into
a private `/tmp`, which the host `codex` CLI could not follow ("failed to
connect to ~/.codex/app-server-control/app-server-control.sock").

---

## Architecture

### Design Principles

- **Stdlib only:** No cobra, pflag, or other CLI frameworks. The help text in the reference is hand-written and doctests verify its exact form; a framework would either break parity or require extensive customization, wasting time. Dispatch is a simple `case` statement.
- **Version injection:** Version info is injected via `-ldflags` into `internal/buildinfo.Version` at build time. `codvps version` is added (not in reference but expected in a Go binary).
- **Exec abstraction:** All subprocess launches (systemctl, claude, codex, gh, git, ssh-keygen, etc.) go through a single `exec.Run(cmd, args)` interface so tests can stub with fake binaries on `PATH`. Logging and error style are handled by this layer.
- **Error handling:** Errors from user commands exit 1 (soft fail, retryable), authentication/login errors exit 2 (soft fail, needs operator action), and catastrophic errors exit 1 with context. The reference behavior is preserved exactly.
- **Logging:** No structured logging; output mirrors the reference (stdout for user-facing text, stderr for warnings/errors).

### Directory Layout

```
cmd/codvps/
  main.go                    # Entry point, dispatch to internal/cli

internal/
  buildinfo/
    buildinfo.go            # Version, commit, build time (injected via ldflags)
  
  cli/
    cli.go                  # Dispatcher and help output
    
  login/
    login.go                # login claude/codex/github and helper functions
    
  repo/
    repo.go                 # repo add/list/remove, registry validation
    registry.go             # Repository registry I/O and validation
    
  head/
    head.go                 # head enable/disable/list/pair codex
    
  config/
    commands.go             # config link/unlink (internal/configlink)
    sync.go                 # Sync planner and applier
    
  components/
    components.go           # components list/status
    
  doctor/
    doctor.go               # doctor health checks
    
  systemd/
    systemd.go              # systemd unit lifecycle (enable/disable/start/stop/restart/status)
    
  state/
    state.go                # Manage ~/.config/codvps/, ~/.local/state/codvps/ paths
    
  exec/
    exec.go                 # Subprocess abstraction; tests stub this with fake binaries

tools/

.gitignore
go.mod
go.sum
Makefile
```

### Packages (internal/*)

**internal/buildinfo:**
- `Version`, `Commit`, `BuildTime` (injected at build, e.g., `-ldflags "-X internal/buildinfo.Version=v1.0"`)
- Minimal; no dependencies

**internal/cli:**
- `Main(args []string) int`: Dispatcher function; parses args, routes to subcommand handler, returns exit code
- `Usage()`: Prints help text (exact match to reference)
- Subcommand entry points: `CmdLoginClaude`, `CmdLoginCodex`, `CmdLoginGitHub`, `CmdRepoAdd`, etc.

**internal/exec:**
- `Run(name string, args ...string) error`: Launch subprocess, capture output, return error if non-zero
- `RunWithIO(name string, args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) error`
- Test stub: `test/testexec/testexec.go` provides a mock registry so tests can record expected calls and provide canned responses

**internal/state:**
- `EnsureStateDir()`: Create ~/.config/codvps/ with correct permissions
- `EnsureStateFile(path string, label string)`: Ensure file exists with mode 0600
- `ReadRegistry()`: Parse repository registry (sorted names)
- `WriteRegistry(names []string)`: Write sorted registry atomically (mktemp + mv)
- `ReadClaudeHeadEnabled()`: Check if flag file exists
- `SetClaudeHeadEnabled(enabled bool)`: Write or remove flag file

**internal/systemd:**
- `Enable(unit string) error`: systemctl --user enable
- `Disable(unit string) error`: systemctl --user disable
- `Start(unit string) error`: systemctl --user start
- `Stop(unit string) error`: systemctl --user stop
- `Restart(unit string) error`: systemctl --user restart
- `DaemonReload() error`: systemctl --user daemon-reload
- `Status(unit string) error`: Check if active/enabled (return error if not)
- `IsActive(unit string) bool`: Returns true if active
- `GetUnitFilePath(unit string) string`: Get installed path (e.g., `/etc/systemd/user/claude-remote@test.service`)

**internal/repo:**
- `Add(url string, checkExisting bool) error`: Clone/reattach repo, update registry, reconcile heads
- `List(sorted bool) ([]RepoInfo, error)`: Return registered repos with paths and Claude head state
- `Remove(name string) error`: Deregister, detach from heads, stop units
- `ValidateName(name string) error`: Enforce naming rules
- `IsPhysicalGitRepo(name string) bool`: Probe filesystem
- `CanonicalRemoteIdentity(url string) (string, error)`: Compute identity (pure Go, table-tested normalization; see below)
- `SeedRepositoryRegistry() error`: One-time adoption for pre-registry hosts

**internal/head:**
- `EnableClaude() error`: Start claude-remote units for all repos
- `DisableClaude() error`: Stop and disable claude-remote units
- `EnableCodex() error`: Start the codex-remote unit (removing a dangling control-socket link left by the retired sandbox)
- `DisableCodex() error`: Stop codex-remote unit
- `ListClaude() (map[string]bool, error)`: Per-repo Claude head state
- `IsCodexRunning() bool`: Probe the daemon by connecting to its control socket
- `CodexPairCode() (string, error)`: Delegate to `codex head pair` and return JSON

**internal/providers:**
- `Provider`, `Set`, `All()`, `Set.Validate()`: one definition per coding CLI and switch (see "Providers: one definition per coding CLI")
- `Set.Runtimes()`, `Set.HeadHosts(heads)`, `NewHeads(...)`: what the commands iterate

**internal/login:**
- `Run(ctx, CLI)`: run a provider's login command (`login.CLI`: executable, arguments, scoped environment, follow-up check)
- `GitHub(mode string, keyChoice string, passphraseEmpty bool, yes bool) error`: Full GitHub login flow (delegating to gh CLI, ssh-keygen, git config)
  - Substeps: preflight check, ensure auth, manage SSH key, register key, known_hosts, SSH config, git identity
  - All output goes to stdout/stderr as in reference
  - Returns exit code 2 if login incomplete and cannot proceed non-interactively

**internal/config:**
- `Connect(source, ref, subdir, profile string) error`: Clone or use local source, propose/use mapping
- `Status() error`: Show connected source and per-target state
- `Diff() error`: Preview changes
- `Sync(yes, yesHooks bool) error`: Apply sync with rollback on error
- `Restore(target string) error`: Restore from receipt
- `Disconnect() error`: Forget source
- Helper functions: validate source, resolve ref commit, compute sync plan, apply files, record receipts

**internal/components:**
- `List() error`: Show installed CLIs and selected switch
- `Status() error`: Probe what is installed and what selection allows

**internal/runtimes** (removed; vendor install/update is `internal/providers/vendor.go`):
- `Worker.Run(action, providers)`, `Publisher.Drain()`, `Pinner.Pin(overrides)`, `Updater.Run(providers, yes)`: the four privilege-separated halves of an update
- `Spec` / `Specs`: one managed runtime each, built by `ClaudeRuntime`, `CodexRuntime` and supplied by the provider set
- `Resolver.ConfiguredBinary/Pinned/Worker/Version`: what each unit is configured to run (`runtime path|exec <provider>`), read-only
- `BuildReport(...)`: the structured runtime table (`status`, every `update` phase)

**internal/doctor:**
- `Report() int`: Run all checks (tools, permissions, state, daemon), return non-zero if any fail
- Checks: codvps state dir ownership, registry format, systemd availability, ssh/gh/git presence, Claude/Codex login state, Codex daemon accessibility (and a dangling control-socket link), unit file permissions

### Build and Release

**Makefile:**
```makefile
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "\
  -X internal/buildinfo.Version=$(VERSION) \
  -X internal/buildinfo.Commit=$(COMMIT) \
  -X internal/buildinfo.BuildTime=$(BUILD_TIME)"

codvps:
	CGO_ENABLED=0 go build $(LDFLAGS) -o codvps ./cmd/codvps

test:
	go test ./...

test-coverage:
	go test -cover ./...

clean:
	rm -f codvps

.PHONY: codvps test test-coverage clean
```

**Distribution:**
- Static binary: `CGO_ENABLED=0 go build` targets linux/amd64 and linux/arm64
- SHA256SUMS file: Distributed alongside binary, signed GPG (same as reference install.sh)
- Bootstrap install.sh: Updated to fetch Go binary instead of using Bash; curl|bash one-liner continues to work
- Pre-existing installations: `codvps install` overwrites the paths it declares and names each file it replaces (see "Pre-existing installations" above); migrating another tool's installation is out of scope

---

## Test Strategy

### Unit Tests (Go)

Every `internal/*` package has a `*_test.go` file:

- **exec tests:** Mock subprocess calls (use `exec.StubRegistry` in tests)
- **state tests:** Temp directories with known file layouts; verify read/write behavior
- **systemd tests:** Mock systemctl calls; verify unit names, error handling
- **repo tests:** Mock registry state, git operations
- **config tests:** Mock source sources (git URLs vs local), plan computation, sync, rollback
- **login tests:** Mock CLI calls (gh, claude, codex, ssh-keygen)

Tests must not call the real systemctl, git, or other external tools. Use `os.Setenv("PATH", testBinDir)` to inject a test PATH with fake binaries.

### Integration Tests (Bash + Go)

Reconstructed fake-VPS suite runs only on GitHub-hosted CI:

- **Smoke tests:** Mirrors `tests/smoke.sh` from reference
  - Install codvps on Docker Ubuntu 24.04 with systemd
  - Run login, repo, head, config workflows
  - Verify systemd units, state files, help output
  - Assert exit codes and exact help text

- **Test cases by owner:**
  - **(Claude heads):** `test-head-enable-claude`, `test-head-disable-claude`, `test-head-list`
  - **(Codex head/watchdog):** `test-head-enable-codex`, `test-head-disable-codex`, `test-head-pair-codex`, `test-codex-watchdog`
  - **(install/doctor):** `test-doctor-checks`, `test-install-prerequisites`
  - **(login github):** `test-login-github-interactive`, `test-login-github-noninteractive`, `test-ssh-key-management`
  - **(config link, supersedes config sync):** `10_config_link.sh`
  - **(components):** `test-components-list`, `test-components-status`, `test-install-components-matrix`
  - **(repo/registry):** `test-repo-add`, `test-repo-list`, `test-repo-remove` (`test-codex-mount-namespace` removed by)
  - **(vendor update, supersedes):** `19_vendor_updates.sh`

### Test Matrix Coverage

| Feature | Unit | Integration | Manual |
|---------|------|-------------|--------|
| Help output (exact text parity) | Yes | Yes (smoke tests) | Not needed |
| Exit codes | Yes | Yes | Not needed |
| State file I/O | Yes | Yes | Not needed |
| systemd unit lifecycle | Mock only | Yes (smoke) | Not needed |
| Codex mount namespace | removed | removed | Not needed |
| GitHub login flow | Mock | Yes (if GH auth available) | Yes (operator interactive) |
| Config sync with rollback | Yes | Yes | Yes (with real config source) |

### Review Findings (from reference)

Review findings carried over from the reference implementation are restated as one-line acceptance items in the "Carried review findings" section of `docs/test-parity.md`; treat that list as binding acceptance criteria.

---

## Distribution

### Build and Packaging

**Static binary:**
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "..." -o codvps-linux-amd64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "..." -o codvps-linux-arm64
sha256sum codvps-linux-* > SHA256SUMS
gpg --detach-sign SHA256SUMS  # Optional, for install.sh verification
```

**install.sh changes:**
- Detect platform (linux-amd64, linux-arm64)
- Download corresponding binary from release
- Verify SHA256SUMS (optional GPG check)
- Place at `/usr/local/bin/codvps` (owned by root, mode 0755)
- Preserve curl|bash one-liner workflow (backward compatibility)

**Pre-existing installations:**
- `codvps install` overwrites the unit files, binary and links it declares and names each one it replaces (see "Pre-existing installations")
- Migrating another tool's installation is out of scope for the public tool; an operator who needs a conflicting unit or link kept moves it first

### Release Cadence and Automation

- Trigger: Manual `git tag` in codvps repo (e.g., `v1.0.0`)
- GitHub Actions: Build static binaries, create release, attach binaries + SHA256SUMS
- Semver: MAJOR.MINOR.PATCH (same as reference)
- Rollback: Previous release always available on GitHub Releases

---

## Open Questions and Risks

1. **Canonical remote identity normalization (ported to Go):** The reference uses a 60+ line Python3 inline script to normalize git URLs into a canonical identity. This is ported to Go as a pure, table-tested function in `internal/repo/canonical.go` to eliminate Python3 runtime dependency (M1.5 goal). The function normalizes these URL forms:
   - **SCP format** (`user@host:path` or `host:path`): Extracts user, host, path. If host is github.com and user is git, outputs `repository:github.com/path`. Otherwise outputs `scp:user@host/path`.
   - **File URLs** (`file:///path` or `file://localhost/path`): Resolves to absolute path, outputs `file:/abs/path`.
   - **HTTP/HTTPS URLs** (`https://host/path`, optionally with auth): Host is lowercased; github.com paths are lowercased. If standard HTTPS (no auth, no port), outputs `repository:host/path`. Otherwise outputs `network:https://user@host:port/path`.
   - **SSH scheme URLs** (`ssh://git@host/path`): If standard SSH (git user, no port), outputs `repository:host/path`. Otherwise outputs `network:ssh://user@host:port/path`.
   - **Git scheme URLs** (`git://host/path`): Standard git protocol, outputs `repository:host/path`.
   - **Relative or bare paths**: Resolved to absolute, outputs `file:/abs/path`.
   All forms strip trailing slashes and `.git` suffix before normalization. GitHub.com host and paths are case-normalized.

2. **Codex mount namespace isolation testing:** removed with the sandbox.

3. **Control-socket liveness:** the reference `codex_remote_daemon_pid()` used `ss` because the socket could be a symlink into a PrivateTmp. With no PrivateTmp the socket is where Codex binds it, so the Go probe simply connects to it.

4. **Ownership record (resolved,):** codvps keeps no ownership record; install is the single manager of the paths it declares and uninstall removes that fixed set (see "Pre-existing installations").

5. **Help text assertion in tests:** The reference help output is tested at the byte level in smoke.sh (`codvps help` output must match exactly). Go's flag package and cobra generate slightly different help text. Codvps uses no framework (stdlib only), so help is hand-written. Tests must be updated to reflect any formatting changes (e.g., column widths, indentation). Confirm the team accepts running a diff on help output as part of test review.

6. **Provider path naming:** Resolved — `/opt/codvps/runtimes/` is used exclusively for all runtime versions (claude, codex; see the State and Config Compatibility Table above). There is no dual-read or legacy path to reconcile.

7. **GitHub authentication flow:** `login github` is complex (8 steps, interactive prompts, non-interactive flags, SSH passphrase choices). The reference is 400+ lines of Bash with careful error handling and multiple fallback paths. Porting to Go should preserve exact behavior, but the code will be quite large. Consider breaking it into smaller functions or a state machine for clarity.

8. **Config sync rollback atomicity:** Superseded by: `config link` only creates symlinks into a git checkout, so there is no apply to roll back; git is the history.

9. **Codex head ensure via watchdog:** The reference uses a system timer (`codex-remote-watchdog.timer`) that runs `codvps head ensure codex` every 60 seconds. This is a passive keep-alive that restarts the daemon if the control socket disappears. In the Go port, `head ensure codex` must be a fast, idempotent check (probes socket, does not restart unless socket is missing). Confirm performance and systemd interaction with the team.

10. **Credential storage for config sources:** `config link` clones through `repo add`, which can clone git URLs that require authentication (SSH keys, GitHub tokens, etc.). The reference does not handle credential prompting for non-interactive sources (it requires an already-configured SSH key or token). The Go port should preserve this behavior: if clone fails, offer guidance (e.g., "set up your SSH key" or "authenticate with `gh auth login`"), do not prompt inline.

---

## References

- Reference implementation: the reference implementation at commit the reference implementation
- Key files:
  - the reference implementation's main CLI script (5348 lines, full CLI)
  - `install.sh` (bootstrap and provisioning)
  - `tests/smoke.sh` (integration test suite)
  - `systemd/` (unit files)
  - `README.md` (architecture and installation)

**Next steps:**
-: Reconstruct fake-VPS test suite in Go (Docker + systemd + integration tests)
- through: Implement subcommands
-: Define and execute state path migration strategy
