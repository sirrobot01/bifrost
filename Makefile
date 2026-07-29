GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf dev)
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build test lint verify docs snapshot

build:
	@mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/bifrost ./cmd/bifrost

test:
	$(GO) test -race ./...

lint:
	$(GO) vet ./...
	golangci-lint run ./...

verify: test lint
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o /tmp/bifrost-linux-amd64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -o /tmp/bifrost-linux-arm64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build -trimpath -o /tmp/bifrost-linux-armv7 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -trimpath -o /tmp/bifrost-darwin-amd64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -trimpath -o /tmp/bifrost-darwin-arm64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 $(GO) build -trimpath -o /tmp/bifrost-freebsd-amd64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 $(GO) build -trimpath -o /tmp/bifrost-freebsd-arm64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=openbsd GOARCH=amd64 $(GO) build -trimpath -o /tmp/bifrost-openbsd-amd64 ./cmd/bifrost
	CGO_ENABLED=0 GOOS=openbsd GOARCH=arm64 $(GO) build -trimpath -o /tmp/bifrost-openbsd-arm64 ./cmd/bifrost

docs:
	npm --prefix docs ci
	npm --prefix docs run check
	npm --prefix docs run build

snapshot:
	goreleaser release --snapshot --clean --skip=sign
