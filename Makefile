.PHONY: build test lint web docker-config dev mcpm-sync mcpm-contract mcpm-lint

MCPM_DIR := mcpm

# Injected into the binaries via -ldflags; falls back to a dirty-tree marker
# when git metadata is unavailable (e.g. shallow exports).
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
VERSION_LDFLAGS := -X main.version=$(VERSION)

build: web
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/toolhub ./cmd/toolhub
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/toolhub-bridge ./cmd/toolhub-bridge
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/toolhub-config-migrate ./cmd/toolhub-config-migrate

web:
	cd web && npm ci && npm run build
	rm -rf cmd/toolhub/dist/assets
	cp -R web/dist/. cmd/toolhub/dist/

test:
	go test ./...
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s packaging/salt/tests -p '*_test.py'
	cd web && npm run typecheck

lint:
	gofmt -w $$(find cmd internal -name '*.go' -type f)
	go vet ./...
	cd web && npm run typecheck

docker-config:
	TOOLHUB_MASTER_KEY=test-only-key TOOLHUB_BOOTSTRAP_USERNAME=admin TOOLHUB_BOOTSTRAP_PASSWORD=test-only-password TOOLHUB_MANAGED_USERNAME=toolhub TOOLHUB_BRIDGE_HMAC_KEY=0123456789abcdef0123456789abcdef TOOLHUB_BRIDGE_GID=999 docker compose config --quiet

dev:
	go run ./cmd/toolhub

mcpm-sync:
	cd $(MCPM_DIR) && uv sync --frozen

mcpm-contract:
	$(MCPM_DIR)/.venv/bin/mcpm toolhub contract --json

mcpm-lint:
	cd $(MCPM_DIR) && uv run ruff check src
