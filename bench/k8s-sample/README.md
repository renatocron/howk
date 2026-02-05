# K8s Sample Manifests (Development/Testing)

These manifests are for development and testing purposes only. They deploy a complete HOWK stack including Redis and Redpanda (Kafka).

## ⚠️ Production Note

For production deployments, **use the Helm chart** (`charts/howk/`) instead, which requires external Redis and Kafka services.

## Quick Start (Development)

```bash

# Fix your image (build+publish)
$EDITOR bench/k8s-sample/kustomization.yaml

# Deploy everything (includes Redis and Redpanda)
kubectl apply -k bench/k8s-sample/

# Wait for pods
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redis -n howk --timeout=120s
kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=redpanda -n howk --timeout=120s

# Port forward API
kubectl port-forward svc/api -n howk 8080:8080

# Test
curl http://localhost:8080/health
```

## Architecture

This sample deploys:
- Redis (single instance)
- Redpanda (Kafka-compatible, single node)
- HOWK API (2 replicas)
- HOWK Worker (3 replicas)
- HOWK Scheduler (1 replica)
- HOWK Reconciler (1 replica)

## Files

| File | Description |
|------|-------------|
| `00-namespace.yaml` | howk namespace |
| `01-configmap.yaml` | HOWK configuration |
| `10-redis.yaml` | Redis deployment |
| `11-redpanda.yaml` | Redpanda (Kafka) deployment |
| `12-topics-init.yaml` | Kafka topic initialization |
| `20-api.yaml` | API deployment |
| `21-worker.yaml` | Worker deployment |
| `22-scheduler.yaml` | Scheduler deployment |
| `23-reconciler.yaml` | Reconciler deployment |
| `kustomization.yaml` | Kustomize configuration |

## Cleanup

```bash
kubectl delete -k bench/k8s-sample/
```
