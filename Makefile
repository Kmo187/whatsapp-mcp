.PHONY: build test lint run docker fmt

VERSION ?= $(shell git describe --tags --always 2>/dev/null || echo dev)

build:
	cd whatsapp-bridge && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.Version=$(VERSION)" -o whatsapp-bridge .
	cd whatsapp-mcp-server && uv sync

test:
	cd whatsapp-bridge && go test -race ./...
	cd whatsapp-mcp-server && uv run pytest -q

lint:
	cd whatsapp-bridge && test -z "$$(gofmt -l .)" && go vet ./...
	cd whatsapp-mcp-server && uv run ruff check . && uv run ruff format --check . && uv run mypy

fmt:
	cd whatsapp-bridge && gofmt -w .
	cd whatsapp-mcp-server && uv run ruff format . && uv run ruff check --fix .

run:
	cd whatsapp-bridge && go run .

docker:
	docker compose up -d --build
