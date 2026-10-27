# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Manual mode: `codvps remote start <claude|cursor> [<repo>] [--background|-d]`,
  `codvps remote stop <claude|cursor> [<repo>]` and
  `codvps remote status [<claude|cursor> [<repo>]]`. Without a repository,
  `start` runs one background worker per registered repository. Background
  workers are detached sessions with pid files and logs under
  `~/.local/state/codvps/remote/`. `remote run` and the Codex forms still work;
  Codex ignores `--background` because its daemon already detaches.
- `codvps internal remote-start --provider <claude|cursor> --repo NAME`, the new
  `ExecStart` of `claude-remote@.service` and `cursor-remote@.service`.

### Changed
- Systemd units and manual mode now share one remote start per provider
  (`session.Start`). Manual `codvps remote run claude` now uses the unit's
  `--spawn worktree --capacity 4 --no-create-session-in-dir
  --remote-control-session-name-prefix cloud-<repo>` flags. Manual
  `remote start codex` runs the unit's bounded-retry helper. The Claude unit's
  effective argv is unchanged.
- `cursor-remote@.service` starts through `internal remote-start` and keeps its
  worker identity (`<hostname>-<repo>`, data under
  `~/.local/share/cursor-agent/codvps-workers/<repo>`), so existing systemd
  Cursor workers are unchanged. Manual mode keeps its `codvps-<repo>` name.
- In manual mode, `enable`/`disable`/`pair`/`ensure` now point to
  `codvps remote start <claude|cursor|codex> [repo] [--background]`.
- `codvps internal shell-exec` is unchanged. Units that are already installed
  and the Codex and OpenCode units still use it.

## [0.1.0] - 2026-10-01

### Added
- Initial public release
- VPS provisioning and setup automation for Ubuntu/Debian
- Support for Claude Code, Codex, Cursor, and OpenCode
- GitHub authentication and SSH key management
- Repository management with git integration
- Systemd service management for remote coding heads
- Component selection system
- Health diagnostics via `doctor` command
- Configuration linking for dotfiles
- Vendor CLI update management
- Bootstrap installer with SHA256 verification

### Changed
- Module path migrated from `github.com/egginsect/codvps` to `github.com/egginsect/codvps`

[Unreleased]: https://github.com/egginsect/codvps/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/egginsect/codvps/releases/tag/v0.1.0
