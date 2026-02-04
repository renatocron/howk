package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaults(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	// Load config with no file and no env vars
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify defaults
	assert.Equal(t, 8080, cfg.API.Port)
	assert.Equal(t, "localhost:9092", cfg.Kafka.Brokers[0])
	assert.Equal(t, "localhost:6379", cfg.Redis.Addr)
	assert.Equal(t, "howk-workers", cfg.Kafka.ConsumerGroup)

	// Verify TTL defaults
	assert.Equal(t, 24*time.Hour, cfg.TTL.CircuitStateTTL)
	assert.Equal(t, 7*24*time.Hour, cfg.TTL.StatusTTL)
	assert.Equal(t, 48*time.Hour, cfg.TTL.StatsTTL)
	assert.Equal(t, 24*time.Hour, cfg.TTL.IdempotencyTTL)
}

func TestLoadConfigFromEnv(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	// Set environment variables
	t.Setenv("HOWK_API_PORT", "9090")
	t.Setenv("HOWK_KAFKA_BROKERS", "kafka1:9092,kafka2:9092")
	t.Setenv("HOWK_REDIS_ADDR", "redis.example.com:6379")
	t.Setenv("HOWK_TTL_STATUS_TTL", "24h")

	// Load config
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify environment variable overrides
	assert.Equal(t, 9090, cfg.API.Port)
	assert.Equal(t, "redis.example.com:6379", cfg.Redis.Addr)

	// Verify TTL override
	assert.Equal(t, 24*time.Hour, cfg.TTL.StatusTTL)
	// Other TTLs should still be defaults
	assert.Equal(t, 24*time.Hour, cfg.TTL.CircuitStateTTL)
	assert.Equal(t, 48*time.Hour, cfg.TTL.StatsTTL)
	assert.Equal(t, 24*time.Hour, cfg.TTL.IdempotencyTTL)
}

func TestLoadConfigFromFile(t *testing.T) {
	// Create a temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write a simple config file
	configContent := `
api:
  port: 7070

redis:
  addr: "redis-prod.example.com:6379"
  password: "secret"

ttl:
  status_ttl: 72h
  circuit_state_ttl: 48h
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Load config from file
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify file values
	assert.Equal(t, 7070, cfg.API.Port)
	assert.Equal(t, "redis-prod.example.com:6379", cfg.Redis.Addr)
	assert.Equal(t, "secret", cfg.Redis.Password)

	// Verify TTL values from file
	assert.Equal(t, 72*time.Hour, cfg.TTL.StatusTTL)
	assert.Equal(t, 48*time.Hour, cfg.TTL.CircuitStateTTL)
	// Other TTLs should still be defaults
	assert.Equal(t, 48*time.Hour, cfg.TTL.StatsTTL)
	assert.Equal(t, 24*time.Hour, cfg.TTL.IdempotencyTTL)
}

func TestLoadConfigEnvPrecedence(t *testing.T) {
	// Create a temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write a config file
	configContent := `
api:
  port: 7070

ttl:
  status_ttl: 72h
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Set environment variables to override file values
	t.Setenv("HOWK_API_PORT", "8888")
	t.Setenv("HOWK_TTL_STATUS_TTL", "36h")

	// Load config
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify environment variables take precedence over file
	assert.Equal(t, 8888, cfg.API.Port)
	assert.Equal(t, 36*time.Hour, cfg.TTL.StatusTTL)
}

func TestLoadConfigMissingFile(t *testing.T) {
	// Try to load a non-existent config file
	cfg, err := LoadConfig("/nonexistent/path/config.yaml")
	// Should get an error since we explicitly specified a file that doesn't exist
	require.Error(t, err)
	assert.Nil(t, cfg)
}

func TestLoadConfigSearchPaths(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	// Create a config.yaml in the current directory
	configContent := `
api:
  port: 6060
`
	err := os.WriteFile("config.yaml", []byte(configContent), 0644)
	require.NoError(t, err)

	// Load config with empty path (should search current directory)
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify it found the file in current directory
	assert.Equal(t, 6060, cfg.API.Port)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	// Create a temporary directory and invalid config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write invalid YAML
	configContent := `invalid: yaml: content: [`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Try to load invalid config
	cfg, err := LoadConfig(configPath)
	assert.Error(t, err)
	assert.Nil(t, cfg)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify all default values are set
	assert.NotNil(t, cfg)
	assert.Equal(t, 8080, cfg.API.Port)
	assert.Equal(t, "localhost:9092", cfg.Kafka.Brokers[0])
	assert.Equal(t, "localhost:6379", cfg.Redis.Addr)
	assert.Equal(t, "howk-workers", cfg.Kafka.ConsumerGroup)

	// Verify TTL defaults
	assert.Equal(t, 24*time.Hour, cfg.TTL.CircuitStateTTL)
	assert.Equal(t, 7*24*time.Hour, cfg.TTL.StatusTTL)
	assert.Equal(t, 48*time.Hour, cfg.TTL.StatsTTL)
	assert.Equal(t, 24*time.Hour, cfg.TTL.IdempotencyTTL)
	// RetryDataTTL uses default of 0 (not explicitly set in DefaultConfig)

	// Verify Kafka topics
	assert.Equal(t, "howk.pending", cfg.Kafka.Topics.Pending)
	assert.Equal(t, "howk.results", cfg.Kafka.Topics.Results)
	assert.Equal(t, "howk.deadletter", cfg.Kafka.Topics.DeadLetter)
	assert.Equal(t, "howk.scripts", cfg.Kafka.Topics.Scripts)
	assert.Equal(t, "howk.slow", cfg.Kafka.Topics.Slow)
}

func TestLoadConfigMultipleBrokers(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	// Set multiple brokers via environment variable
	t.Setenv("HOWK_KAFKA_BROKERS", "kafka1:9092,kafka2:9092,kafka3:9092")

	// Load config
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify all brokers are parsed
	assert.Len(t, cfg.Kafka.Brokers, 3)
	assert.Equal(t, "kafka1:9092", cfg.Kafka.Brokers[0])
	assert.Equal(t, "kafka2:9092", cfg.Kafka.Brokers[1])
	assert.Equal(t, "kafka3:9092", cfg.Kafka.Brokers[2])
}

func TestLoadConfigEmptyBrokers(t *testing.T) {
	// Create a temporary directory for the test
	tmpDir := t.TempDir()
	originalDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(originalDir)

	// Set empty brokers (should use default)
	t.Setenv("HOWK_KAFKA_BROKERS", "")

	// Load config
	cfg, err := LoadConfig("")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Should fall back to default
	assert.Len(t, cfg.Kafka.Brokers, 1)
	assert.Equal(t, "localhost:9092", cfg.Kafka.Brokers[0])
}

func TestLoadConfigComplexValues(t *testing.T) {
	// Create a temporary directory and config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	// Write a comprehensive config file
	configContent := `
api:
  port: 7070
  read_timeout: 10s
  write_timeout: 20s
  max_request_size: 2097152

kafka:
  brokers:
    - "kafka1:9092"
    - "kafka2:9092"
  topics:
    pending: "custom.pending"
    results: "custom.results"
    deadletter: "custom.dl"
    scripts: "custom.scripts"
    slow: "custom.slow"
  consumer_group: "custom-workers"
  retention: 336h
  producer_batch_size: 1000
  producer_linger_ms: 100
  producer_compression: "snappy"
  consumer_fetch_min_bytes: 1024
  consumer_fetch_max_wait: 500ms
  group_session_timeout: 30s
  group_heartbeat_interval: 10s
  group_rebalance_timeout: 60s

redis:
  addr: "redis.example.com:6380"
  password: "secret123"
  db: 1
  pool_size: 20
  min_idle_conns: 5
  dial_timeout: 10s
  read_timeout: 5s
  write_timeout: 5s

delivery:
  timeout: 60s
  max_idle_conns: 200
  max_conns_per_host: 50
  idle_conn_timeout: 120s
  tls_handshake_timeout: 15s
  user_agent: "custom-agent/1.0"

retry:
  max_attempts: 5
  base_delay: 2s
  max_delay: 2h
  jitter: 0.2

circuit_breaker:
  failure_threshold: 10
  recovery_timeout: 60s
  half_open_max_calls: 5

scheduler:
  interval: 5s
  batch_size: 500

ttl:
  circuit_state_ttl: 2h
  status_ttl: 48h
  stats_ttl: 168h
  idempotency_ttl: 48h
  retry_data_ttl: 96h

lua:
  enabled: true
  script_ttl: 10m
  max_execution_time: 60s
  max_memory: 2097152

concurrency:
  worker_pool_size: 20
  webhook_queue_size: 2000
  max_inflight_per_endpoint: 200
  circuit_check_interval: 10s
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	// Load config
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Verify API settings
	assert.Equal(t, 7070, cfg.API.Port)
	assert.Equal(t, 10*time.Second, cfg.API.ReadTimeout)
	assert.Equal(t, 20*time.Second, cfg.API.WriteTimeout)
	assert.Equal(t, int64(2097152), cfg.API.MaxRequestSize)

	// Verify Kafka settings
	assert.Len(t, cfg.Kafka.Brokers, 2)
	assert.Equal(t, "custom.pending", cfg.Kafka.Topics.Pending)
	assert.Equal(t, "custom-workers", cfg.Kafka.ConsumerGroup)
	assert.Equal(t, 336*time.Hour, cfg.Kafka.Retention)
	assert.Equal(t, 1000, cfg.Kafka.ProducerBatchSize)
	assert.Equal(t, 100, cfg.Kafka.ProducerLingerMs)
	assert.Equal(t, "snappy", cfg.Kafka.ProducerCompression)

	// Verify Redis settings
	assert.Equal(t, "redis.example.com:6380", cfg.Redis.Addr)
	assert.Equal(t, "secret123", cfg.Redis.Password)
	assert.Equal(t, 1, cfg.Redis.DB)

	// Verify other complex settings
	assert.Equal(t, 5, cfg.Retry.MaxAttempts)
	assert.Equal(t, 10, cfg.CircuitBreaker.FailureThreshold)
	assert.Equal(t, 200, cfg.Concurrency.MaxInflightPerEndpoint)
}
