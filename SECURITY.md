# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.x     | :white_check_mark: |

## Reporting a Vulnerability

Please report security vulnerabilities via:
- **GitHub Security Advisories** (preferred): [Report here](https://github.com/egginsect/codvps/security/advisories/new)

Do not open public issues for security vulnerabilities.

## Security Model

codvps runs on your VPS with elevated privileges (sudo for initial install). It:
- Stores no credentials itself (defers to gh CLI keyring/file storage)
- Binds services to 127.0.0.1 only (access via SSH tunnel)
- Configures UFW firewall (allow SSH, deny other incoming)
- Uses bubblewrap + AppArmor for sandboxing

Third-party CLI installers (Claude, Codex, Cursor, OpenCode) are executed with operator privileges. This is a best-effort, single-trusted-operator scope security model.
