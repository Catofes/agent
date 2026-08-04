.PHONY: build test check run dev check-env

GOCACHE ?= /tmp/classroom-agent-gocache
GOMODCACHE ?= /tmp/classroom-agent-gomodcache
ENV_FILE ?= .env
ENV_FILE_PATH := $(if $(filter /%,$(ENV_FILE)),$(ENV_FILE),./$(ENV_FILE))

build:
	mkdir -p build
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go build -trimpath -ldflags "-s -w -X main.version=$$(git rev-parse --short HEAD 2>/dev/null || echo dev)" -o build/classroom-agent .

test:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test ./...

check:
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go test -race ./...
	GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE) go vet ./...

check-env:
	@test -f "$(ENV_FILE_PATH)" || { echo "找不到 $(ENV_FILE_PATH)，请先创建环境变量文件" >&2; exit 1; }

run: check-env build
	@set -a; . "$(ENV_FILE_PATH)"; set +a; exec ./build/classroom-agent

dev: check-env
	@set -a; . "$(ENV_FILE_PATH)"; set +a; exec env GOCACHE="$(GOCACHE)" GOMODCACHE="$(GOMODCACHE)" go run .
