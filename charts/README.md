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

### Redis Sentinel (High Availability)

When using Redis with Sentinel for automatic failover, configure `sentinelAddrs`
instead of `addr`:

```yaml
externalServices:
  redis:
    addr: ""                              # ignored when sentinel is configured
    password: ""
    sentinelAddrs:
      - "rfs-redis.redis.svc:26379"       # Sentinel endpoints
    sentinelMasterName: "mymaster"         # Sentinel master name
```

This uses go-redis `FailoverClient` for automatic master discovery and failover.

### ArgoCD Deployment

When deploying via ArgoCD with a single source and inline Helm values:

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: howk
  namespace: argocd
spec:
  source:
    repoURL: https://github.com/renatocron/howk.git
    targetRevision: v0.2.0
    path: charts/howk
    helm:
      values: |
        images:
          api:
            tag: v0.2.0
          worker:
            tag: v0.2.0
          scheduler:
            tag: v0.2.0
          reconciler:
            tag: v0.2.0
        externalServices:
          kafka:
            brokers:
              - "my-kafka-0.my-kafka-brokers.kafka.svc:9092"
          redis:
            sentinelAddrs:
              - "rfs-redis.redis.svc:26379"
            sentinelMasterName: "mymaster"
  destination:
    server: https://kubernetes.default.svc
    namespace: howk
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=true
```

### Kafka Topics

HOWK requires 6 Kafka topics. Create them before deploying. You can customize
topic names via `config.kafka.topics` in values:

```yaml
config:
  kafka:
    topics:
      pending: my-howk-pending
      results: my-howk-results
      deadletter: my-howk-deadletter
      scripts: my-howk-scripts
      slow: my-howk-slow
      state: my-howk-state
```

Example topic creation for a 3-broker cluster:

```bash
BROKER=localhost:9092

# Standard topics
for topic in my-howk-pending my-howk-results my-howk-slow; do
  kafka-topics.sh --bootstrap-server $BROKER \
    --create --topic $topic --partitions 6 --replication-factor 3 --if-not-exists
done

kafka-topics.sh --bootstrap-server $BROKER \
  --create --topic my-howk-deadletter --partitions 1 --replication-factor 3 --if-not-exists

# Compacted topics
kafka-topics.sh --bootstrap-server $BROKER \
  --create --topic my-howk-scripts --partitions 1 --replication-factor 3 --if-not-exists \
  --config cleanup.policy=compact --config compression.type=snappy

kafka-topics.sh --bootstrap-server $BROKER \
  --create --topic my-howk-state --partitions 1 --replication-factor 3 --if-not-exists \
  --config cleanup.policy=compact --config segment.bytes=104857600 --config min.cleanable.dirty.ratio=0.01
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

## Transformer Feature (Incoming Webhooks)

Transformers enable API-side Lua scripting for incoming webhook fan-out. Route, transform, and distribute a single incoming request to multiple destinations.

### Enable Transformers

```yaml
config:
  transformer:
    enabled: true
    scriptDirs:
      - "/etc/howk/transformers"
    timeout: "500ms"
    memoryLimitMb: 50

# Define your transformer scripts
transformerScripts:
  stripe-router: |
    local ok, event = pcall(json.decode, incoming)
    if not ok then return end
    
    -- Route to billing
    if event.type == "charge.succeeded" then
      howk.post("https://billing.internal/webhook", event)
    end
    
    -- Always send to analytics
    howk.post("https://analytics.internal/track", event)

# Optional: JSON configs per script
transformerConfigs:
  stripe-router: |
    {"allowed_domains": ["billing.internal", "analytics.internal"]}

# Optional: Basic Auth
transformerAuth:
  stripe-router: |
    admin:$2b$12$xxxxxxxx...
```

### Accessing Transformers

```bash
curl -X POST http://localhost:8080/incoming/stripe-router \
  -H "Content-Type: application/json" \
  -u admin:password \
  -d '{"type": "charge.succeeded", "amount": 1000}'
```

Response:
```json
{
  "webhooks": [
    {"id": "wh_01JHX...", "endpoint": "https://billing.internal/webhook"},
    {"id": "wh_01JHY...", "endpoint": "https://analytics.internal/track"}
  ],
  "count": 2
}
```

### ConfigMap Auto-Reload

When transformer scripts are updated, HOWK can reload them without pod restart.

#### Option 1: Annotation-Based (Default, Recommended)

```yaml
configmapReload:
  enabled: true
  method: annotation  # Triggers rolling restart on ConfigMap change
```

When you update `transformerScripts` in values and run `helm upgrade`, pods automatically restart with new scripts.

#### Option 2: Signal-Based (Hot Reload without Restart)

```yaml
configmapReload:
  enabled: true
  method: signal      # Sends SIGHUP to reload scripts in-place
  sleepTime: 15       # Check interval in seconds
```

This uses a sidecar container that watches ConfigMaps and sends SIGHUP to the API process for true zero-downtime reload.

#### Option 3: Manual Reload

```bash
# Send SIGHUP to all API pods
kubectl exec deploy/howk-api -- kill -HUP 1

# Or restart deployment
kubectl rollout restart deployment/howk-api
```

See [docs/transformers.md](../docs/transformers.md) for complete Lua API documentation and examples.

## Uninstall

```bash
helm uninstall howk -n howk
```

## Development

For local testing with embedded Redis/Kafka, see `bench/k8s-sample/`.
