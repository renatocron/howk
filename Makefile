.PHONY: all build run-api run-worker run-scheduler test clean deps infra infra-down test-coverage-ci check-infra

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

# Coverage packages to exclude (test utilities and mocks only)
COVERAGE_EXCLUDES := cmd/ internal/mocks/ internal/testutil/

# Filter coverage file to exclude certain packages
define filter_coverage
	grep -v -E "$(shell echo $(COVERAGE_EXCLUDES) | tr ' ' '|')" coverage.out > coverage.filtered.out
	mv coverage.filtered.out coverage.out
endef



# Unit test coverage
test-unit-coverage:
	go test -short -coverprofile=coverage.out -coverpkg=./... ./...
	$(call filter_coverage)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	go tool cover -func=coverage.out | grep total

# Integration tests (requires infrastructure)
test-integration:
	go test -v -race -count=1 -tags=integration ./...

# E2E tests (requires all services)
test-e2e:
	go test -v -race -tags=e2e ./...

# Run all tests
test-all: test-unit test-integration test-e2e

# CI test pipeline (unit + integration)
test-ci: infra
	@echo "Waiting for services..."
	@sleep 15
	go test -v -race -count=1 -tags=integration -timeout=10m ./...

test-coverage:
	go test -coverprofile=coverage.out -coverpkg=./... ./...
	$(call filter_coverage)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | grep total

# Check if infrastructure is running (Kafka on 19092, Redis on 6380)
check-infra:
	@echo "Checking infrastructure..."
	@if ! nc -z localhost 19092 2>/dev/null; then \
		echo "Kafka not running on port 19092. Starting infrastructure..."; \
		$(MAKE) infra; \
		echo "Waiting for services to be ready..."; \
		sleep 15; \
	elif ! nc -z localhost 6380 2>/dev/null; then \
		echo "Redis not running on port 6380. Starting infrastructure..."; \
		$(MAKE) infra; \
		echo "Waiting for services to be ready..."; \
		sleep 15; \
	else \
		echo "Infrastructure is already running!"; \
	fi

# Combined coverage for CI (unit + integration merged, auto-starts infrastructure if needed)
test-coverage-ci: check-infra .ensure-gocovmerge
	@echo "Running unit tests..."
	go test -race -short -coverprofile=coverage_unit.out -coverpkg=./... -count=1 ./...
	@echo "Running integration tests..."
	go test -race -coverprofile=coverage_integration.out -coverpkg=./... -count=1 -tags=integration -timeout=10m ./...
	@echo "Merging coverage reports..."
	@./scripts/merge-coverage.sh coverage_unit.out coverage_integration.out > coverage.out
	$(call filter_coverage)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
	@go tool cover -func=coverage.out | grep total
	@rm -f coverage_unit.out coverage_integration.out

# Install gocovmerge if not present
.ensure-gocovmerge:
	@which gocovmerge > /dev/null 2>&1 || (echo "Installing gocovmerge..." && go install github.com/wadey/gocovmerge@latest)

# Local coverage with merged unit + integration (for development)
test-coverage-full: check-infra .ensure-gocovmerge
	@echo "Running unit tests..."
	go test -short -coverprofile=coverage_unit.out -coverpkg=./... ./...
	@echo "Running integration tests..."
	go test -coverprofile=coverage_integration.out -coverpkg=./... -tags=integration ./...
	@echo "Merging coverage reports..."
	@./scripts/merge-coverage.sh coverage_unit.out coverage_integration.out > coverage.out
	$(call filter_coverage)
	go tool cover -html=coverage.out -o coverage.html
	@echo "Full coverage report (unit + integration): coverage.html"
	@go tool cover -func=coverage.out | grep total
	@rm -f coverage_unit.out coverage_integration.out

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
	rm -f coverage.out coverage.html coverage_unit.out coverage_integration.out coverage.filtered.out
