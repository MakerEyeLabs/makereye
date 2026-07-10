MODULE     := github.com/MakerEyeLabs/makereye
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X $(MODULE)/internal/version.Version=$(VERSION) \
           -X $(MODULE)/internal/version.Commit=$(COMMIT) \
           -X $(MODULE)/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: build build-arm64 test vet fmt fmt-check check clean install

build:
	go build -ldflags "$(LDFLAGS)" -o bin/makereye ./cmd/makereye

# Cross-compile for Raspberry Pi OS Lite 64-bit (arm64).
build-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/makereye-linux-arm64 ./cmd/makereye

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed on:" && gofmt -l . && exit 1)

check: fmt-check vet test

clean:
	rm -rf bin

# Developer convenience: build and install the binary locally (not the
# full appliance install; see scripts/install.sh for that).
install: build
	install -m 0755 bin/makereye /usr/local/bin/makereye
