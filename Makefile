REPO ?= notipswe/notip-infra
TAG ?= main
FILE ?= nats-contracts.yaml
SERVICE ?= data-consumer

.PHONY: fetch-contracts test integration-test lint fmt all

fetch-contracts:
	@echo "Fetching contracts..."
	bash scripts/generate-asyncapi.sh --repo $(REPO) --tag $(TAG) --file $(FILE) --service $(SERVICE)

test:
	@echo "Executing unit tests..."
	go test -coverprofile=coverage.out ./...

integration-test:
	@echo "Executing integration tests..."
	go test -tags=integration -timeout=5m ./tests/integration/...

lint:
	@echo "Executing Linter..."
	golangci-lint run

fmt:
	@echo "Formatting code..."
	go fmt ./...

all: fmt lint test integration-test