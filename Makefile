.PHONY: all build run-api run-worker run-scheduler test clean deps infra infra-down

# Build all binaries
all: build

build:
	go build -o bin/howk-api ./cmd/api
	go build -o bin/howk-worker ./cmd/worker
	go build -o bin/howk-scheduler ./cmd/scheduler

# Run individual components
run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker

run-scheduler:
	go run ./cmd/scheduler

# Run all components together (for development)
run-all:
	@echo "Starting all components..."
	@make run-api &
	@sleep 2
	@make run-worker &
	@sleep 1
	@make run-scheduler &
	@wait

# Tests
test-unit:
	go test -v -race -short ./...

test: test-unit

# Unit test coverage
test-unit-coverage:
	go test -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	go tool cover -func=coverage.out | grep total

# Integration tests (requires infrastructure)
test-integration:
	go test -v -race -tags=integration ./...

# E2E tests (requires all services)
test-e2e:
	go test -v -race -tags=e2e ./...

# Run all tests
test-all: test-unit test-integration test-e2e

# CI test pipeline (unit + integration)
test-ci: infra
	@echo "Waiting for services..."
	@sleep 15
	go test -v -race -short ./...
	go test -v -race -tags=integration ./...

test-coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

# Dependencies
deps:
	go mod download
	go mod tidy

# Infrastructure
infra:
	docker-compose up -d
	@echo "Waiting for services to be ready..."
	@sleep 10
	@echo "Infrastructure ready!"
	@echo "  Kafka: localhost:19092"
	@echo "  Redis: localhost:6380 (Docker)"
	@echo "  Console: http://localhost:8888"
	@echo "  Echo server: http://localhost:8090"

infra-down:
	docker-compose down

infra-clean:
	docker-compose down -v

# Development helpers
lint:
	golangci-lint run

fmt:
	gofmt -s -w .

# Quick test with curl
test-enqueue:
	curl -X POST http://localhost:8080/webhooks/tenant123/enqueue \
		-H "Content-Type: application/json" \
		-d '{"endpoint": "http://localhost:8090/webhook", "payload": {"event": "test.event", "data": {"id": 123}}}'

test-status:
	@echo "Usage: make test-status ID=wh_xxx"
	curl http://localhost:8080/webhooks/$(ID)/status

test-stats:
	curl http://localhost:8080/stats

# Clean up
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html
