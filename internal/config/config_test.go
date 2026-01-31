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
