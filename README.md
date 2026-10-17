# codvps

A headless CLI-first host manager for native Claude Code and Codex remote control on Linux VPS instances.

**Status:** Pre-alpha. Command interfaces and behavior are subject to change.

## Overview

codvps provides a command-line interface for managing remote Claude Code and Codex environments on a Linux VPS. It handles authentication, repository management, model synchronization, and host configuration.

## Install

On an Ubuntu/Debian VPS, as a sudo-capable login user (not root):

```bash
curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh | bash
```

Pin a release and choose components up front:

```bash
curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh \
  | bash -s -- --version v0.1.0 --components claude,codex --switch none
```

`install.sh` only downloads the codvps binary for your architecture (linux
amd64 or arm64), verifies it against the release's `SHA256SUMS` and the
version it reports, and keeps it at `~/.local/share/codvps/<version>/codvps`.
It then shows what will change and asks before running
`sudo codvps install`, which provisions the host (apt packages, gh, Node.js,
the selected coding CLIs, the bubblewrap AppArmor profile, swap, ufw; each
step printed before it runs) and installs the codvps units. Without a
terminal it prints that command instead of running it. Afterwards:

```bash
codvps login github
```

`login github` needs no terminal: when you are not logged in it starts GitHub's
device flow and prints the URL and one-time code (also over `docker exec` or
`ssh` without `-t`), each prompt takes its default answer without a terminal
(`--yes` does the same on one), and a missing or too-old `gh` is installed or
upgraded first.

### Inspect first

```bash
curl -fsSLO https://github.com/egginsect/codvps/releases/download/v0.1.0/install.sh
curl -fsSLO https://github.com/egginsect/codvps/releases/download/v0.1.0/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
less install.sh
bash install.sh --version v0.1.0
```

### Without install.sh

Download and verify the release assets yourself, then run the installer
from the verified binary:

```bash
v=v0.1.0 arch=amd64   # or arm64
curl -fsSLO "https://github.com/egginsect/codvps/releases/download/$v/codvps-linux-$arch"
curl -fsSLO "https://github.com/egginsect/codvps/releases/download/$v/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
chmod +x "codvps-linux-$arch"
sudo "./codvps-linux-$arch" install --components claude,codex --switch none
```

Or build from the source tarball (Go toolchain required):

```bash
curl -fsSL "https://github.com/egginsect/codvps/archive/refs/tags/$v.tar.gz" | tar -xz
cd "codvps-${v#v}" && make build && sudo ./codvps install
```

`sudo codvps install --skip-provision` wires up codvps on a host that is
already provisioned.

## Build from source

```bash
make build
./codvps --help
```

## Workspaces and native CLI sessions

Choose where **new** checkouts live, or choose a complete path for one clone:

```bash
codvps config set repo_root /workspace/shared/repos
codvps repo add https://github.com/OWNER/REPO.git
codvps repo add https://github.com/OWNER/OTHER.git --dest /workspace/shared/other-checkout
codvps repo list
codvps run claude REPO
codvps run codex REPO -- --help
codvps run cursor REPO
```

`run` launches the installed native CLI in the registered checkout and passes
arguments after `--` literally. Provider trust, sandbox and permission prompts
remain native. The configured root defaults to `$HOME`. Changing it never moves
or remaps existing repositories: old name-only entries continue to mean
`$HOME/<name>`, and new registrations persist their absolute path. A name cannot
be registered at two destinations. Symlinked checkout paths and linked Git
worktrees are refused. Removing a registration keeps the checkout.

Settings are non-secret values in `$XDG_CONFIG_HOME/codvps/settings.json`
(default `~/.config/codvps/settings.json`). Supported keys are `repo_root`,
`github_config_dir`, `git_config_global`, and `session_mode`:

```bash
codvps config get repo_root
codvps config unset repo_root
```

Existing services must be reinstalled with the updated binary/templates to use
external checkout paths. The updated Claude/Cursor launcher resolves each path
from the registry, including Claude workspace trust and Cursor's worker directory.
Once explicit paths are saved, the registry uses versioned JSON. Older codvps
binaries do not support that format; do not downgrade without backing up state
and explicitly rebuilding a compatible registry. No checkout is moved by an
upgrade.

### GitHub without VPS SSH setup

```bash
codvps login github --auth-only
codvps login github --auth-only --non-interactive
```

`--auth-only` uses an existing GitHub CLI (including gh 2.46), performs native
GitHub HTTPS authentication, sets its GitHub protocol to HTTPS and configures
its Git credential helper. It never provisions the host, generates/registers
SSH keys, or changes Git author identity. `--non-interactive` only verifies
an existing account and never writes login/helper configuration. `--defer`
prints a read-only preflight.

Optionally reuse an isolated GitHub configuration and Git config file:

```bash
codvps config set github_config_dir /workspace/shared/codvps-auth
codvps config set git_config_global /workspace/shared/codvps-auth/gitconfig
```

These settings apply `GH_CONFIG_DIR` and `GIT_CONFIG_GLOBAL` to codvps's child
Git/GitHub/provider commands. codvps does not read, copy or print credentials.
GitHub continues to own credential storage. Without these settings, the ordinary
shell/GitHub configuration is used. The original `login github` guided VPS SSH
setup remains available when `--auth-only` is omitted.

### Hosts without systemd: manual mode

Use the same codvps binary with already-installed native provider CLIs. No
privileged installer or service manager is required for this mode:

```bash
codvps config set session_mode manual
codvps config set repo_root /workspace/shared/repos
codvps login github --auth-only
codvps repo add https://github.com/OWNER/REPO.git
codvps login claude
codvps login codex
codvps login cursor
codvps doctor
```

Manual mode never installs a missing provider CLI as a side effect. Install it
from the provider's official distribution first. It also does not seed Claude
Remote Control consent or automatically trust repositories. `repo add/list/remove`
do not reconcile any systemd services in this mode. `status` and `doctor`
distinguish local prerequisite checks from live authentication/remote checks.
Changing `session_mode` does **not** stop existing services: disable any previously
enabled heads before switching to avoid duplicate workers.

Run repository-scoped remote workers in separate terminals:

```bash
codvps remote run claude REPO
codvps remote run cursor REPO
```

These commands hand the terminal and signals to the native CLI. They do not
create a background supervisor, restart crashed workers or survive a terminal
closure automatically. Stop foreground workers with Ctrl+C. A normal VPS can
continue to use the existing systemd `enable`/`disable` commands in the default
`systemd` mode.

Codex uses one **host-wide** native daemon and the same `~/.codex` home as its
codvps login and interactive CLI:

```bash
codvps remote start codex
codvps remote pair codex
codvps remote status codex
codvps remote stop codex
```

`start` requires the official standalone Codex package under `~/.codex` so the
vendor's daemon bootstrap does not copy an unrelated CLI/runtime directory.
`stop` affects that Codex home's shared native daemon and may interrupt other
clients using it. No repository argument is accepted for Codex Remote: select
the checkout directory in the connected client. A listening socket is only a
local health check; it does not prove pairing, host registration, task visibility,
or that a user can inspect/steer the same task in their Codex UI. Those require
an actual connected client and a real task created on the registered Remote.
An ordinary local `codex exec` invocation is not evidence of that integration.

Cursor Remote requires a plan with Cloud Agents. Code and context are sent to
Cursor, and its worker has the operator account's filesystem access beyond the
selected checkout. A working directory is not a sandbox. No provider login,
remote pairing or trust setting is enabled merely by cloning a repository.

## Verification boundaries

`make test` covers local Git integration, command dispatch, paths, authentication
routing and hermetic provider/service behavior. The fake-VPS Docker suite uses
fake provider backends and tests systemd wiring; it is not live provider E2E.
A restricted command executor without systemd or Unix sockets cannot validate
live remote startup. Its restrictions say nothing about another process namespace
on a host that happens to share the same filesystem. Validate login, remote
startup, simultaneous provider use, and a visible/steerable client task separately
on the actual authorized execution environment.

## Privacy

codvps does not collect telemetry, analytics, or usage data. Network requests are limited to:
- Downloading vendor CLI installers (Claude, Codex, Cursor, OpenCode) from their official sources
- apt package installation (GitHub CLI, Node.js via NodeSource)
- User-initiated git/ssh operations to GitHub

All coding assistant traffic (Claude API, OpenAI API, Cursor remote) is direct from your VPS to the provider; codvps does not proxy or observe it.

## Support

This is a best-effort open-source project. For issues and questions:
- Check existing [GitHub Issues](https://github.com/egginsect/codvps/issues)
- Open a new issue with reproduction steps
- See [SECURITY.md](SECURITY.md) for vulnerability reporting

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for details.
