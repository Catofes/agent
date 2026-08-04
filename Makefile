.PHONY: build test check run

GOCACHE ?= /tmp/classroom-agent-gocache
GOMODCACHE ?= /tmp/classroom-agent-gomodcache

build:
	mkdir -p build
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -trimpath -ldflags "-s -w -X main.version=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o build/classroom-agent .

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...

check:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -race ./...
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go vet ./...

run:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run .
