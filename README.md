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
