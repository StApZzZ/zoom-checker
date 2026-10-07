SHELL := /bin/bash
export PATH := $(HOME)/.local/go/bin:$(HOME)/go/bin:$(PATH)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -s -w -X zoom-checker/internal/version.Version=$(VERSION) -X zoom-checker/internal/version.Commit=$(COMMIT)
GOFLAGS := -trimpath
BIN     := bin/zoom-checker

.PHONY: all build web go run dev test test-pg test-install lint e2e release package docker clean

all: build

## build: frontend + backend into ./bin/zoom-checker
build: web go

# Vite empties web/dist; .gitkeep keeps `//go:embed all:dist` building on a fresh clone.
web:
	cd web && ( [ -d node_modules ] || npm ci ) && npm run build && touch dist/.gitkeep

go:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/zoom-checker

## run: build and start with a local ./data directory
run: build
	ZOOM_WEBHOOK_SECRET_TOKEN=$${ZOOM_WEBHOOK_SECRET_TOKEN:-dev-secret} ADMIN_PASSWORD=$${ADMIN_PASSWORD:-admin} ./$(BIN)

## dev: backend on :8080 + Vite dev server on :5173 (proxying /api)
dev:
	( ZOOM_WEBHOOK_SECRET_TOKEN=dev-secret ADMIN_PASSWORD=admin go run ./cmd/zoom-checker & ) ; cd web && npm run dev

test:
	go test -race ./...

## test-pg: run the store tests against a throwaway PostgreSQL container
test-pg:
	docker rm -f zc-test-pg >/dev/null 2>&1 || true
	docker run -d --name zc-test-pg -e POSTGRES_PASSWORD=test -e POSTGRES_DB=zc -p 55432:5432 postgres:18-alpine >/dev/null
	until docker exec zc-test-pg pg_isready -U postgres >/dev/null 2>&1; do sleep 1; done
	TEST_POSTGRES_URL='postgres://postgres:test@localhost:55432/zc?sslmode=disable' go test ./internal/store/... ./internal/db/... ; rc=$$?; docker rm -f zc-test-pg >/dev/null; exit $$rc

## test-install: run deploy/install.sh end to end in a throwaway systemd container
## (Docker; install, upgrade, secret input, checksums, firewall, uninstall)
test-install: go
	bash deploy/test/install-test.sh

lint:
	go vet ./...
	cd web && npm run lint
	@if command -v shellcheck >/dev/null; then shellcheck -x deploy/install.sh deploy/test/install-test.sh; \
	else echo "shellcheck not installed: skipping deploy/*.sh"; fi

e2e: build
	cd web && npx playwright test

## release: install bundle in ./dist — static binaries for linux amd64/arm64, install.sh,
## the systemd unit and the settings template (install.sh picks the binary for its machine)
release: web
	rm -rf dist && mkdir -p dist
	set -e; for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o dist/zoom-checker-linux-$$arch ./cmd/zoom-checker ; \
	done
	install -m 0755 deploy/install.sh dist/install.sh
	install -m 0644 deploy/zoom-checker.service deploy/.env.example dist/
	cd dist && sha256sum zoom-checker-linux-* install.sh zoom-checker.service .env.example > SHA256SUMS

## package: the release bundle as one archive (dist/zoom-checker-linux.tar.gz); one file keeps the
## hidden .env.example from getting lost on the way to the server
## (files owned by root: extracted by root, they must not stay writable by the builder's uid)
package: release
	owner=$$(tar --version 2>/dev/null | grep -q 'GNU tar' && echo '--owner=0 --group=0 --numeric-owner' || echo '--uid 0 --gid 0'); \
	tar $$owner -czf dist/zoom-checker-linux.tar.gz -C dist zoom-checker-linux-amd64 zoom-checker-linux-arm64 \
		install.sh zoom-checker.service .env.example SHA256SUMS

docker:
	docker build -t zoom-checker:$(VERSION) -t zoom-checker:latest .

clean:
	rm -rf bin dist
