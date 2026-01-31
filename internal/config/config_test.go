package config_test

import (
	"testing"
	"time"

	"github.com/howk/howk/internal/config"
	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.DefaultConfig()

	assert.NotNil(t, cfg)

	// API
	assert.Equal(t, 8080, cfg.API.Port)
	assert.Equal(t, 10*time.Second, cfg.API.ReadTimeout)

	// Kafka
	assert.Equal(t, []string{"localhost:9092"}, cfg.Kafka.Brokers)
	assert.Equal(t, "howk.pending", cfg.Kafka.Topics.Pending)

	// Redis
	assert.Equal(t, "localhost:6379", cfg.Redis.Addr)

	// Delivery
	assert.Equal(t, 30*time.Second, cfg.Delivery.Timeout)
	assert.Equal(t, "HOWK/1.0", cfg.Delivery.UserAgent)

	// Retry
	assert.Equal(t, 20, cfg.Retry.MaxAttempts)
	assert.Equal(t, 0.2, cfg.Retry.Jitter)

	// CircuitBreaker
	assert.Equal(t, 5, cfg.CircuitBreaker.FailureThreshold)
	assert.Equal(t, 5*time.Minute, cfg.CircuitBreaker.RecoveryTimeout)

	// Scheduler
	assert.Equal(t, 1*time.Second, cfg.Scheduler.PollInterval)
	assert.Equal(t, 500, cfg.Scheduler.BatchSize)
}
