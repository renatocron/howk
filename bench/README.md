# HOWK Benchmark Results

This directory contains benchmark results for the HOWK webhook processing system.

## Test Environment

- **Kubernetes Cluster**: DigitalOcean Kubernetes
- **Nodes**: 4 nodes, 4GB RAM each
- **HOWK Version**: Latest (built from source)
- **Date**: 2026-02-05

## System Configuration

### Optimized Settings (High Throughput)

```yaml
kafka:
  producer_batch_size: 32768      # 32KB batches
  producer_linger_ms: 200          # 200ms linger for batching
  producer_compression: snappy

redis:
  pool_size: 200
  min_idle_conns: 20

concurrency:
  max_inflight_per_endpoint: 100
  slow_lane_rate: 50
```

## Benchmark Results

### Test 1: Single Benchmark (5,000 req/sec target)

**Configuration:**
- Workers: 10
- Batch size: 20 requests per tick
- Rate: 20 ticks/sec per worker
- Target: 4,000 req/sec
- Duration: 2 minutes

**Results:**
```
Total Requests: 63,620
Successful: 63,619 (100.00%)
Failed: 1 (0.00%)
Throughput: 530.17 req/sec
Average Latency: 292 ms
Min Latency: 10 ms
Max Latency: 5,650 ms
```

### Test 2: Parallel Benchmarks (15,000 req/sec target)

**Configuration:**
- 3 parallel benchmark jobs
- Each: 10 workers × 25 batch × 20 rate = 5,000 req/sec target
- Aggregate target: 15,000 req/sec
- Duration: 3 minutes

**Results:**

| Job | Requests | Throughput | Latency (avg) | Success Rate |
|-----|----------|------------|---------------|--------------|
| #1 | 99,950 | 555.28 req/sec | 330 ms | 100% |
| #2 | 104,350 | 579.72 req/sec | 318 ms | 100% |
| #3 | 101,500 | 563.89 req/sec | 327 ms | 100% |
| **Total** | **305,800** | **1,698.89 req/sec** | **~325 ms** | **100%** |

## Performance Analysis

### Bottleneck Identification

**CPU Usage during peak load:**

| Component | CPU Usage | Status |
|-----------|-----------|--------|
| API Pods | 1-100m | ✅ Low utilization |
| Workers | 3-9m | ✅ Low utilization |
| Redis | 212m | ⚠️ Moderate usage |
| Redpanda (Kafka) | 11m | ✅ Low utilization |
| Echo Server | 0-10m | ✅ Low utilization |
| **Stress Test (3 workers)** | **~1,500m** | ⚠️ **BOTTLENECK** |

**Conclusion**: Further testing was halted as the stress test client itself became the performance bottleneck at ~1,700 req/sec, consuming approximately 1.5 CPU cores each across 3 parallel workers. The HOWK system (API, workers, Redis, and Kafka) all showed moderate to low CPU utilization, indicating significant capacity remains. To properly stress test the system beyond this point would require optimizing the load generator or deploying additional benchmark pods.

### Throughput Scaling

```
Single benchmark:     ~530 req/sec
Parallel (3x):       ~1,700 req/sec
```

The system scales linearly with multiple concurrent benchmark clients, indicating the bottleneck is not in the HTTP handling but in the message queuing/processing pipeline.

## Key Findings

1. **Sustainable Throughput**: ~1,700 req/sec with 100% success rate
2. **Latency**: Average ~325ms at peak load
3. **Load Generator Limitation**: The Go stress test client became the bottleneck at ~1,700 req/sec (1.5 CPU)
4. **System Headroom**: HOWK components (API, workers, Redis, Kafka) all showed moderate to low CPU utilization, indicating the system can handle significantly higher loads
5. **Testing Constraint**: Further stress testing requires optimizing the benchmark tool or deploying more load generators
5. **API Scaling**: API pods have room for 4-6x more traffic

## Recommendations for Higher Throughput

1. **Scale Redis**: Use Redis Cluster or a larger Redis instance
2. **Increase API Replicas**: Scale to 4-6 pods for better load distribution
3. **Increase Worker Replicas**: Scale to 6-9 pods for faster processing
4. **Tune Batch Size**: Current 200ms linger_ms provides good batching

## How to Reproduce

### Prerequisites
- Kubernetes cluster
- kubectl configured
- HOWK images built and available in your registry

### Deploy HOWK
```bash
kubectl apply -k k8s/
```

### Run Benchmark
```bash
# Single benchmark
kubectl apply -f bench/benchmark-job.yaml

# Parallel benchmarks
kubectl apply -f bench/benchmark-parallel.yaml
```

### Monitor Results
```bash
kubectl top pods -n howk
kubectl logs job/benchmark -n howk
```

## Files

- `benchmark-job.yaml` - Single benchmark job manifest
- `benchmark-parallel.yaml` - Parallel benchmark jobs manifest
- `stress-test/` - Stress test tool source code
