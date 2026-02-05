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

# =============================================================================
# Docker Hub Publishing
# =============================================================================

DOCKER_HUB_USER ?= renatocron
VERSION ?= latest

# Build all images for Docker Hub
dockerhub-build:
	@echo "Building images for Docker Hub ($(DOCKER_HUB_USER))..."
	docker build --target api -t $(DOCKER_HUB_USER)/howk-api:$(VERSION) .
	docker build --target worker -t $(DOCKER_HUB_USER)/howk-worker:$(VERSION) .
	docker build --target scheduler -t $(DOCKER_HUB_USER)/howk-scheduler:$(VERSION) .
	docker build --target reconciler -t $(DOCKER_HUB_USER)/howk-reconciler:$(VERSION) .

# Push all images to Docker Hub
dockerhub-push: dockerhub-build
	@echo "Pushing images to Docker Hub..."
	docker push $(DOCKER_HUB_USER)/howk-api:$(VERSION)
	docker push $(DOCKER_HUB_USER)/howk-worker:$(VERSION)
	docker push $(DOCKER_HUB_USER)/howk-scheduler:$(VERSION)
	docker push $(DOCKER_HUB_USER)/howk-reconciler:$(VERSION)

# Build and push multi-arch images (requires buildx)
dockerhub-buildx:
	@echo "Building multi-arch images for Docker Hub..."
	docker buildx build --platform linux/amd64,linux/arm64 --target api -t $(DOCKER_HUB_USER)/howk-api:$(VERSION) --push .
	docker buildx build --platform linux/amd64,linux/arm64 --target worker -t $(DOCKER_HUB_USER)/howk-worker:$(VERSION) --push .
	docker buildx build --platform linux/amd64,linux/arm64 --target scheduler -t $(DOCKER_HUB_USER)/howk-scheduler:$(VERSION) --push .
	docker buildx build --platform linux/amd64,linux/arm64 --target reconciler -t $(DOCKER_HUB_USER)/howk-reconciler:$(VERSION) --push .

# Tag and push as latest
dockerhub-release: VERSION=latest
dockerhub-release: dockerhub-push
	@echo "Released $(VERSION) to Docker Hub"

# =============================================================================
# Helm Chart
# =============================================================================

# Package Helm chart
helm-package:
	@echo "Packaging Helm chart..."
	helm package charts/howk -d charts/

# Install HOWK via Helm
helm-install:
	@echo "Installing HOWK via Helm..."
	helm upgrade --install howk ./charts/howk -n howk --create-namespace

# Install with custom values
helm-install-custom:
	@echo "Installing HOWK with custom values..."
	helm upgrade --install howk ./charts/howk -n howk --create-namespace -f my-values.yaml

# Uninstall HOWK
helm-uninstall:
	@echo "Uninstalling HOWK..."
	helm uninstall howk -n howk

# Lint Helm chart
helm-lint:
	@echo "Linting Helm chart..."
	helm lint charts/howk

# Template Helm chart (for debugging)
helm-template:
	@echo "Rendering Helm templates..."
	helm template howk ./charts/howk

# =============================================================================
# Kubernetes Operations
# =============================================================================

# Build Docker images for Kubernetes
docker-build:
	docker build --target api -t howk:api-latest .
	docker build --target worker -t howk:worker-latest .
	docker build --target scheduler -t howk:scheduler-latest .
	docker build --target reconciler -t howk:reconciler-latest .

docker-build-api:
	docker build --target api -t howk:latest .

# Build stress test image
docker-build-stress:
	docker build -t howk-stress-test:latest -f k8s/stress-test/Dockerfile k8s/stress-test/

# Deploy to Kubernetes
k8s-deploy: docker-build-api
	kubectl apply -k k8s/
	@echo "Waiting for infrastructure..."
	kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redis -n howk --timeout=120s
	kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redpanda -n howk --timeout=120s
	@echo "Waiting for topic initialization..."
	kubectl wait --for=condition=complete job/redpanda-topics-init -n howk --timeout=120s || true
	@echo "Deploying HOWK services..."
	kubectl apply -k k8s/
	@echo ""
	@echo "Deployment complete! Services:"
	@echo "  API: kubectl port-forward svc/api -n howk 8080:8080"

# Deploy stress test infrastructure
k8s-deploy-stress: docker-build-stress
	kubectl apply -k k8s/stress-test/
	@echo "Stress test deployed!"

# Run stress test
k8s-stress-test: docker-build-stress
	kubectl delete job stress-test -n howk 2>/dev/null || true
	kubectl apply -k k8s/stress-test/
	@echo "Waiting for stress test to complete..."
	kubectl wait --for=condition=complete job/stress-test -n howk --timeout=300s
	@echo "Stress test logs:"
	kubectl logs job/stress-test -n howk

# View stress test logs
k8s-stress-logs:
	kubectl logs job/stress-test -n howk

# Delete all resources
k8s-delete:
	kubectl delete -k k8s/stress-test/ --ignore-not-found=true
	kubectl delete -k k8s/ --ignore-not-found=true

# Port forward API for local access
k8s-port-forward:
	@echo "Forwarding API to localhost:8080..."
	kubectl port-forward svc/api -n howk 8080:8080

# View logs
k8s-logs-api:
	kubectl logs -l app.kubernetes.io/name=api -n howk --tail=100 -f

k8s-logs-worker:
	kubectl logs -l app.kubernetes.io/name=worker -n howk --tail=100 -f

k8s-logs-scheduler:
	kubectl logs -l app.kubernetes.io/name=scheduler -n howk --tail=100 -f

# Scale workers
k8s-scale-workers:
	@echo "Usage: make k8s-scale-workers REPLICAS=5"
	kubectl scale deployment worker -n howk --replicas=$(REPLICAS)

# Check status
k8s-status:
	@echo "=== Pods ==="
	kubectl get pods -n howk
	@echo ""
	@echo "=== Services ==="
	kubectl get svc -n howk
	@echo ""
	@echo "=== Jobs ==="
	kubectl get jobs -n howk

# Full reset (delete and redeploy)
k8s-reset: k8s-delete
	@echo "Waiting for cleanup..."
	@sleep 5
	$(MAKE) k8s-deploy
