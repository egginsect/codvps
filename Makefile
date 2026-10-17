.PHONY: build build-all install test test-install test-bootstrap test-cli-install vet lint shellcheck checksums clean fakevps

BINARY_NAME := codvps
GO ?= go
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= unknown
PREFIX ?= $(HOME)/.local
DESTDIR ?=

LDFLAGS := -ldflags "-X github.com/egginsect/codvps/internal/buildinfo.Version=$(VERSION) \
	-X github.com/egginsect/codvps/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/egginsect/codvps/internal/buildinfo.BuildDate=$(BUILD_DATE)"

build:
	CGO_ENABLED=0 "$(GO)" build -trimpath $(LDFLAGS) -o $(BINARY_NAME) ./cmd/codvps

# Install only the CLI. VPS provisioning remains `sudo codvps install`.
install: build
	tools/install-cli.sh "./$(BINARY_NAME)" "$(PREFIX)" "$(DESTDIR)"

build-all: clean
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$(GO)" build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/codvps
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$(GO)" build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/codvps

test: test-bootstrap
	"$(GO)" test ./...

test-install: test-bootstrap test-cli-install

# install.sh against a local release fixture, every privileged and network
# command stubbed .
test-bootstrap:
	test/install/bootstrap_test.sh

test-cli-install:
	test/install/cli_test.sh

vet:
	"$(GO)" vet ./...

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
