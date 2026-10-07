.PHONY: build build-all test test-bootstrap vet lint shellcheck checksums clean fakevps

BINARY_NAME := codvps
VERSION ?= dev
COMMIT ?= unknown
BUILD_DATE ?= unknown

LDFLAGS := -ldflags "-X github.com/egginsect/codvps/internal/buildinfo.Version=$(VERSION) \
	-X github.com/egginsect/codvps/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/egginsect/codvps/internal/buildinfo.BuildDate=$(BUILD_DATE)"

build:
	CGO_ENABLED=0 go build -trimpath $(LDFLAGS) -o $(BINARY_NAME) ./cmd/codvps

build-all: clean
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-amd64 ./cmd/codvps
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath $(LDFLAGS) -o dist/$(BINARY_NAME)-linux-arm64 ./cmd/codvps

test: test-bootstrap
	go test ./...

# install.sh against a local release fixture, every privileged and network
# command stubbed .
test-bootstrap:
	test/install/bootstrap_test.sh

vet:
	go vet ./...

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
