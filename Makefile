.PHONY: build build-all install test test-install test-bootstrap test-cli-install test-source-install vet lint shellcheck checksums clean fakevps

BINARY_NAME := codvps
GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= unknown
ifeq ($(origin PREFIX),undefined)
PREFIX := $(value HOME)/.local
endif
DESTDIR ?=

# Command-line/environment variables are normally recursive in make, including
# when exported to recipe processes. Freeze path values without interpreting
# their contents as make expressions before either export or recipe expansion.
override GO := $(value GO)
override PREFIX := $(value PREFIX)
override DESTDIR := $(value DESTDIR)

# Keep user-selected paths literal through both make and the shell. $(value)
# avoids interpreting dollar signs as make syntax; single quoting protects shell
# metacharacters (including quotes, substitutions and spaces).
shell_quote = '$(subst ','"'"',$(1))'

LDFLAGS := -ldflags "-X github.com/egginsect/codvps/internal/buildinfo.Version=$(VERSION) \
	-X github.com/egginsect/codvps/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/egginsect/codvps/internal/buildinfo.BuildDate=$(BUILD_DATE)"

build:
	CGO_ENABLED=0 $(call shell_quote,$(value GO)) build -trimpath $(LDFLAGS) -o $(BINARY_NAME) ./cmd/codvps

# Install only the CLI. VPS provisioning remains `sudo codvps install --system`.
install: build
	tools/install-cli.sh "./$(BINARY_NAME)" $(call shell_quote,$(value PREFIX)) $(call shell_quote,$(value DESTDIR))

build-all: clean
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(call shell_quote,$(value GO)) build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/codvps
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(call shell_quote,$(value GO)) build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/codvps

test: test-bootstrap
	$(call shell_quote,$(value GO)) test ./...

test-install: test-bootstrap test-cli-install test-source-install

# install.sh against a local release fixture, every privileged and network
# command stubbed .
test-bootstrap:
	test/install/bootstrap_test.sh

test-cli-install:
	test/install/cli_test.sh

test-source-install:
	test/install/source_test.sh

vet:
	$(call shell_quote,$(value GO)) vet ./...

lint:
	golangci-lint run

shellcheck:
	@if find . -type f \( -name "*.sh" -o -path "*/.githooks/*" \) 2>/dev/null | grep -q .; then \
		shellcheck $$(find . -type f \( -name "*.sh" -o -path "*/.githooks/*" \) 2>/dev/null); \
	else \
		echo "No shell files found"; \
	fi

checksums: build-all
	cp install.sh dist/install.sh
	cd dist && sha256sum $(BINARY_NAME)-linux-amd64 $(BINARY_NAME)-linux-arm64 install.sh > SHA256SUMS

clean:
	rm -rf dist/ $(BINARY_NAME) *.test coverage.out

fakevps:
	@if [ -z "$$CODVPS_ALLOW_DOCKER" ] || [ "$$CODVPS_ALLOW_DOCKER" != "1" ]; then \
		echo "error: fakevps target requires CODVPS_ALLOW_DOCKER=1"; \
		echo "use: CODVPS_ALLOW_DOCKER=1 make fakevps"; \
		exit 1; \
	fi
	test/fakevps/run.sh
