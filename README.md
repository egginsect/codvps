# codvps

A headless CLI-first host manager for native Claude Code and Codex remote control on Linux VPS instances.

**Status:** Pre-alpha. Command interfaces and behavior are subject to change.

## Overview

codvps provides a command-line interface for managing remote Claude Code and Codex environments on a Linux VPS. It handles authentication, repository management, model synchronization, and host configuration.

## Install

### CLI only, from an existing source checkout

To install the current checkout (including a checked-out pull request), run:

```bash
./install.sh
```

This requires Linux, Go 1.27.1 or newer (see `go.mod`), Make, Bash and the
GNU coreutils (including `timeout`). The script finds an installed, compatible
Go compiler automatically, builds the current source and installs
the real `codvps` executable at `~/.local/bin/codvps`. It does not clone another
checkout, require sudo, install provider CLIs, provision the host, start services,
or change authentication and configuration. It uses your ordinary HOME and XDG
directories when invoked; it does not migrate credentials from another profile.

If `~/.local/bin` is not already on PATH, add it for this shell:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

The installer prints the exact installed path and a command to verify it. It
also prints PATH guidance when needed, but never edits shell startup files.
Once the installation directory is on PATH, run `codvps help`. For
another installation prefix or a packaging staging directory:

```bash
PREFIX=/absolute/prefix ./install.sh
PREFIX=/usr/local DESTDIR=/absolute/staging-directory ./install.sh
```

Discovery checks all absolute PATH directories, `GOROOT`, common user/system
Go locations, and an optional companion runtime at
`../codvps-cli-runtime/toolchain/go/bin/go` relative to the checkout. It checks
each candidate's actual version, skips unrelated programs named `go` and older
compilers, and does not search the filesystem recursively. An explicit `GO`
override is also supported and must be valid. No compiler is downloaded: if no
compatible compiler is found, install the required Go version and rerun
`./install.sh`. Go's automatic toolchain switching is disabled for this build.

`make install` remains available for development and packaging when a valid
Go compiler is already on PATH (or selected with `GO`).

The target is `PREFIX/bin/codvps`, or `DESTDIR/PREFIX/bin/codvps` when staging.
An identical installed binary is left in place; a different regular binary is
backed up before atomic replacement. Symlinked destinations and non-file targets
are refused. Installing the CLI does not enable remote access; use the
[manual-mode setup](#hosts-without-systemd-manual-mode) below where appropriate.

### Native user CLI installation (default)

After installing codvps, run `codvps install` as your normal user. The default
installs Claude Code, standalone Codex, and Cursor CLI from their official
installers into their standard user locations. Existing user installations are
verified with `--version` and preserved. No login or remote access is enabled.

```bash
codvps install --dry-run
codvps install
codvps install --components claude,codex,cursor
```

`--user` is an optional explicit alias for this default. This mode never invokes
sudo, apt, systemd, linger, host provisioning, or the system component registry.
It does not change session mode or stop existing services. Configure manual mode
separately for a host without systemd. Provider installers may update their own
user files and shell PATH; existing HOME and XDG settings are not replaced.

Host provisioning now requires explicit `codvps install --system`, run via sudo.
Legacy system flags (`--switch`, `--skip-provision`) are rejected without
`--system`; they never implicitly select privileged installation.

### Full VPS installation

From a source checkout, use `./install.sh --vps` to explicitly select the
release/VPS bootstrap instead of the source CLI installer. Existing release
flags such as `--version` also select that bootstrap for compatibility. A
missing compiler or incomplete source checkout never falls back to provisioning.

On an Ubuntu/Debian VPS, as a sudo-capable login user (not root):

```bash
curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh | bash
```

Pin a release and choose components up front:

```bash
curl -fsSL https://github.com/egginsect/codvps/releases/latest/download/install.sh \
  | bash -s -- --version v0.1.0 --components claude,codex --switch none
```

The standalone/downloaded or piped script downloads the codvps binary for your
architecture (linux amd64 or arm64), verifies it against the release's `SHA256SUMS` and the
version it reports, and keeps it at `~/.local/share/codvps/<version>/codvps`.
It then shows what will change and asks before running
`sudo codvps install --system`, which provisions the host (apt packages, gh, Node.js,
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
sudo "./codvps-linux-$arch" install --system --components claude,codex --switch none
```

Or build from the source tarball (Go toolchain required):

```bash
curl -fsSL "https://github.com/egginsect/codvps/archive/refs/tags/$v.tar.gz" | tar -xz
cd "codvps-${v#v}" && make build && sudo ./codvps install --system
```

`sudo codvps install --system --skip-provision` wires up codvps on a host that is
already provisioned.

## Build from source

```bash
make build
./codvps help
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

### Register an existing local GitHub checkout offline

```bash
codvps repo add-local /absolute/path/to/existing-checkout
codvps run claude REPO
```

`add-local` reads the checkout's GitHub `origin` locally and registers its
absolute path. It never runs gh, clone, fetch, or ls-remote, and never reconciles
or starts provider heads. No GitHub login or network access is needed. Dirty
files, branches and remotes are preserved. A missing path is an error, never a
reason to clone. Conventional github.com HTTPS and Git SSH origins are accepted;
credentials embedded in URLs, other hosts, aliases and custom ports are refused.
The path must be the top level of a primary non-bare checkout; symlinked paths,
linked worktrees and submodules are refused. Existing name/path registrations
cannot be silently remapped.

This checks the configured origin, not current remote existence, ownership or
access. Later fetch/push operations need their own network access and Git
credentials; provider authentication and Remote pairing remain separate.
The older `repo add URL --dest PATH` can also reattach an existing checkout,
but checks remote reachability with `git ls-remote`.

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

When `gh` is configured to use HTTPS for `github.com`, `repo add` also accepts
conventional SSH URLs such as `git@github.com:OWNER/REPO.git` and
`ssh://git@github.com/OWNER/REPO.git`, using HTTPS for that operation. This
does not change Git configuration or an existing checkout's origin. If `gh`
is unavailable or configured for SSH, the supplied URL is used unchanged;
other hosts, custom SSH users and explicit ports are always left unchanged.

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
