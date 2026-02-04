//go:build !integration

package app

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrap_Success(t *testing.T) {
	// Create a temporary config file
	configContent := `
api:
  port: 8080
  read_timeout: 5s
  write_timeout: 10s
  max_request_size: 1048576
kafka:
  brokers:
    - "localhost:9092"
  topics:
    pending: "webhooks.pending"
    results: "webhooks.results"
    deadletter: "webhooks.dl"
    scripts: "scripts.commands"
    slow: "webhooks.slow"
  consumer_group: "howk-worker"
  retention: 168h
redis:
  addr: "localhost:6379"
  password: ""
  db: 0
  pool_size: 10
  min_idle_conns: 5
  dial_timeout: 5s
  read_timeout: 3s
  write_timeout: 3s
delivery:
  timeout: 30s
  max_idle_conns: 100
  max_conns_per_host: 10
  idle_conn_timeout: 90s
  tls_handshake_timeout: 10s
  user_agent: "howk/1.0"
retry:
  max_attempts: 3
  base_delay: 1s
  max_delay: 1h
  jitter: 0.1
circuit_breaker:
  failure_threshold: 5
  recovery_timeout: 30s
  half_open_max_calls: 3
scheduler:
  interval: 1s
  batch_size: 100
ttl:
  circuit_state_ttl: 1h
  status_ttl: 24h
  stats_ttl: 168h
  idempotency_ttl: 24h
  retry_data_ttl: 72h
lua:
  enabled: true
  script_ttl: 5m
  max_execution_time: 30s
  max_memory: 1048576
concurrency:
  worker_pool_size: 10
  webhook_queue_size: 1000
  max_inflight_per_endpoint: 100
  circuit_check_interval: 5s
`
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(configContent)
	require.NoError(t, err)
	tmpFile.Close()

	// Test Bootstrap
	cfg, err := Bootstrap(tmpFile.Name())
	require.NoError(t, err)
	assert.NotNil(t, cfg)
	assert.Equal(t, 8080, cfg.API.Port)
}

func TestBootstrap_FileNotFound(t *testing.T) {
	cfg, err := Bootstrap("/nonexistent/config.yaml")
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestBootstrap_InvalidConfig(t *testing.T) {
	// Create an invalid config file
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString("invalid: yaml: content: [")
	require.NoError(t, err)
	tmpFile.Close()

	cfg, err := Bootstrap(tmpFile.Name())
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestSetupSignalHandler(t *testing.T) {
	ctx, cancel := SetupSignalHandler()
	defer cancel()

	// Verify context is not done initially
	select {
	case <-ctx.Done():
		t.Fatal("Context should not be done initially")
	default:
		// Expected
	}

	// Send a signal to trigger cancellation
	// We need to send the signal to our own process
	go func() {
		time.Sleep(100 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	}()

	// Wait for context to be cancelled
	select {
	case <-ctx.Done():
		// Expected - context was cancelled
		assert.Error(t, ctx.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("Context should have been cancelled by signal")
	}
}

func TestSetupSignalHandler_Interrupt(t *testing.T) {
	ctx, cancel := SetupSignalHandler()
	defer cancel()

	// Send SIGINT instead of SIGTERM
	go func() {
		time.Sleep(100 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	// Wait for context to be cancelled
	select {
	case <-ctx.Done():
		// Expected - context was cancelled
		assert.Error(t, ctx.Err())
	case <-time.After(2 * time.Second):
		t.Fatal("Context should have been cancelled by SIGINT")
	}
}
