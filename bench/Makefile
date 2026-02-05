.PHONY: build run run-parallel clean

# Build stress test image
build:
	docker build -t howk-stress:latest .

# Run single benchmark
run:
	kubectl apply -f benchmark-job.yaml
	kubectl wait --for=condition=complete job/howk-benchmark -n howk --timeout=300s
	kubectl logs job/howk-benchmark -n howk

# Run parallel benchmarks (3 concurrent)
run-parallel:
	kubectl apply -f benchmark-parallel.yaml
	kubectl wait --for=condition=complete job/howk-benchmark-parallel-1 -n howk --timeout=300s
	kubectl wait --for=condition=complete job/howk-benchmark-parallel-2 -n howk --timeout=300s
	kubectl wait --for=condition=complete job/howk-benchmark-parallel-3 -n howk --timeout=300s
	@echo "=== Results ==="
	kubectl logs job/howk-benchmark-parallel-1 -n howk | tail -10
	kubectl logs job/howk-benchmark-parallel-2 -n howk | tail -10
	kubectl logs job/howk-benchmark-parallel-3 -n howk | tail -10

# Clean up benchmark jobs
clean:
	kubectl delete job/howk-benchmark job/howk-benchmark-parallel-1 job/howk-benchmark-parallel-2 job/howk-benchmark-parallel-3 -n howk --ignore-not-found=true

# Monitor system during benchmark
monitor:
	watch kubectl top pods -n howk
