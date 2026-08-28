.PHONY: build release test test-js check smoke smoke-real run dev check-env

GOCACHE ?= /tmp/classroom-agent-gocache
GOMODCACHE ?= $(shell go env GOMODCACHE)
ENV_FILE ?= .env
ENV_FILE_PATH := $(if $(filter /%,$(ENV_FILE)),$(ENV_FILE),./$(ENV_FILE))
TARGET_OS ?= linux
TARGET_ARCH ?= amd64
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

build:
	mkdir -p build
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o build/classroom-agent .

release:
	mkdir -p build
	CGO_ENABLED=0 GOOS=$(TARGET_OS) GOARCH=$(TARGET_ARCH) GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o build/classroom-agent-$(TARGET_OS)-$(TARGET_ARCH) .
	cd build && sha256sum classroom-agent-$(TARGET_OS)-$(TARGET_ARCH) > classroom-agent-$(TARGET_OS)-$(TARGET_ARCH).sha256

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...
	node --test web/*.test.js

test-js:
	node --test web/*.test.js

check:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -race ./...
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go vet ./...
	node --test web/*.test.js

smoke:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./cmd/smoke

smoke-real:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go run ./cmd/smoke -real -students=5

check-env:
	@test -f "$(ENV_FILE_PATH)" || { echo "找不到 $(ENV_FILE_PATH)，请先创建环境变量文件" >&2; exit 1; }

run: check-env build
	@set -a; . "$(ENV_FILE_PATH)"; set +a; exec ./build/classroom-agent

dev: check-env
	@set -a; . "$(ENV_FILE_PATH)"; set +a; exec env GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go run .
