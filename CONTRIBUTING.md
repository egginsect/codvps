# Contributing to codvps

## Building and Testing

### Build the binary

```bash
make build
```

### Install only the CLI from this checkout

```bash
make install
```

Requires the Go version in `go.mod`, Make, Bash and GNU utilities on Linux.
The default target is `~/.local/bin/codvps`; `PREFIX` and `DESTDIR` are supported.
This does not run the privileged VPS installer or alter provider configuration.
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
