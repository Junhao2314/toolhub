.PHONY: build test lint web docker-config dev

build: web
	go build -o bin/toolhub ./cmd/toolhub
	go build -o bin/toolhub-bridge ./cmd/toolhub-bridge
	go build -o bin/toolhub-config-migrate ./cmd/toolhub-config-migrate

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
