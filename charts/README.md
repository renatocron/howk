# HOWK Helm Chart

High-performance webhook processing with guaranteed delivery.

## Prerequisites

- Kubernetes 1.24+
- Helm 3.8+
- **Redis** (required)
- **Kafka** (required)

## Quick Start

### 1. Install Dependencies

```bash
# Add Helm repos
helm repo add bitnami https://charts.bitnami.com/bitnami
helm repo update

# Install Redis
helm install redis bitnami/redis \
  --set auth.enabled=false \
  --set architecture=standalone

# Install Kafka (single node for testing)
helm install kafka bitnami/kafka \
  --set replicaCount=1 \
  --set zookeeper.replicaCount=1
```

### 2. Install HOWK

```bash
helm upgrade --install howk ./charts/howk \
  --namespace howk \
  --create-namespace
```

### 3. Access API

```bash
kubectl port-forward svc/howk-api 8080:8080 -n howk

curl http://localhost:8080/health
curl http://localhost:8080/ready
```

### 4. Send Webhook

```bash
curl -X POST http://localhost:8080/webhooks/test/enqueue \
  -H "Content-Type: application/json" \
  -d '{"endpoint":"https://httpbin.org/post","payload":{"event":"test"}}'
```

## Configuration

### External Services (Required)

```yaml
externalServices:
  redis:
    addr: "redis-master:6379"    # Your Redis endpoint
    password: ""                  # Password (if auth enabled)
  
  kafka:
    brokers:
      - "kafka:9092"             # Your Kafka brokers
```

### Scaling

```yaml
# Scale up for higher throughput
api:
  replicaCount: 3

worker:
  replicaCount: 6
```

Benchmarked at **~500 RPS** with single replicas.

## Uninstall

```bash
helm uninstall howk -n howk
```

## Development

For local testing with embedded Redis/Kafka, see `bench/k8s-sample/`.
