# loco — build targets. Everything is pure Go: no cgo, no build step, no DLLs.

BIN     := loco
PKG     := ./cmd/loco
VERSION := $(shell sed -n 's/^const Version = "\(.*\)"$$/\1/p' internal/cli/cli.go)
FLAGS   := -trimpath -ldflags="-s -w"

.PHONY: all build test check dist clean install

all: build

build:
	go build $(FLAGS) -o $(BIN) $(PKG)

test:
	go test ./...

# what CI should run: formatting, vet, tests, and a Windows cross-build
check: test
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...

install:
	go install $(FLAGS) $(PKG)

# single self-contained binary per platform, no runtime required
dist: clean
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(FLAGS) -o dist/loco-$(VERSION)-windows-amd64.exe $(PKG)
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(FLAGS) -o dist/loco-$(VERSION)-windows-arm64.exe $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(FLAGS) -o dist/loco-$(VERSION)-linux-amd64     $(PKG)
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(FLAGS) -o dist/loco-$(VERSION)-linux-arm64     $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(FLAGS) -o dist/loco-$(VERSION)-darwin-amd64    $(PKG)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(FLAGS) -o dist/loco-$(VERSION)-darwin-arm64    $(PKG)
	@cd dist && sha256sum * > SHA256SUMS
	@ls -lh dist/

clean:
	rm -rf dist $(BIN) $(BIN).exe
