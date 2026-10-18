# Contributing to codvps

## Building and Testing

### Build the binary

```bash
make build
```

### Install only the CLI from this checkout

```bash
./install.sh
```

Requires the Go version in `go.mod`, Make, Bash and GNU coreutils (including
`timeout`) on Linux.
The script automatically discovers and validates an installed Go compiler; no
compiler is downloaded. It searches absolute PATH entries, `GOROOT`, common Go
locations and the optional sibling `codvps-cli-runtime/toolchain/go/bin/go`.
An explicit `GO` is authoritative and must be valid. Missing or incompatible Go
fails with a prerequisite message, without invoking the VPS bootstrap.
The default target is `~/.local/bin/codvps`; `PREFIX` and `DESTDIR` are supported.
For example, `PREFIX=/absolute/prefix ./install.sh`. The underlying
`make install` target is still available when Go is already selected.
This does not run the privileged VPS installer or alter provider configuration.
The separate release/VPS bootstrap is available with `./install.sh --vps`.
Installer fixtures are included in `make test` and can be run separately with
`make test-install`.

### Run tests

```bash
make test
```

### Run linters and checks

```bash
make vet
make lint
make shellcheck
```

## Developer Certificate of Origin

By contributing to this project, you agree to the Developer Certificate of Origin (DCO). All commits must include a `Signed-off-by:` line matching your full name:

```bash
git commit -s
```
