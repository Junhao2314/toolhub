.PHONY: build test lint web docker-config dev

build: web
	go build -o bin/toolhub ./cmd/toolhub
	go build -o bin/toolhub-agent ./cmd/toolhub-agent

web:
	cd web && npm ci && npm run build
	rm -rf cmd/toolhub/dist/assets
	cp -R web/dist/. cmd/toolhub/dist/

test:
	go test ./...
	cd web && npm run typecheck

lint:
	gofmt -w $$(find cmd internal -name '*.go' -type f)
	go vet ./...
	cd web && npm run typecheck

docker-config:
	TOOLHUB_MASTER_KEY=test-only-key TOOLHUB_BOOTSTRAP_ADMIN_EMAIL=admin@example.com TOOLHUB_BOOTSTRAP_ADMIN_PASSWORD=test-only-password docker compose config --quiet

dev:
	go run ./cmd/toolhub
