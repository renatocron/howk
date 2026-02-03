//go:build integration

package testutil

import (
    "context"
    "os"
    "testing"
    "github.com/howk/howk/internal/config"
    "github.com/howk/howk/internal/hotstate"
)

// SetupRedis connects to Redis (uses TEST_REDIS_ADDR env var, defaults to localhost:6380)
// Uses default circuit breaker config. Use SetupRedisWithCircuitConfig if you need custom circuit breaker settings.
func SetupRedis(t *testing.T) *hotstate.RedisHotState {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6380" // Default for docker-compose exposed port (6380 to avoid conflict with local Redis)
	}

	cfg := config.RedisConfig{
		Addr: addr,
		DB:   15, // Use test DB
	}
	return SetupRedisWithConfig(t, cfg)
}

// SetupRedisWithCircuitConfig connects to Redis with custom circuit breaker config
func SetupRedisWithCircuitConfig(t *testing.T, cbConfig config.CircuitBreakerConfig) *hotstate.RedisHotState {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6380" // Default for docker-compose exposed port (6380 to avoid conflict with local Redis)
	}

	cfg := config.RedisConfig{
		Addr: addr,
		DB:   15, // Use test DB
	}
	return SetupRedisWithFullConfig(t, cfg, cbConfig)
}

// SetupRedisWithConfig allows custom Redis config (uses default circuit breaker config)
func SetupRedisWithConfig(t *testing.T, cfg config.RedisConfig) *hotstate.RedisHotState {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }

    // Use default config values for tests
    defaultCfg := config.DefaultConfig()
    cbConfig := defaultCfg.CircuitBreaker
    ttlConfig := defaultCfg.TTL

    return setupRedisInternal(t, cfg, cbConfig, ttlConfig)
}

// SetupRedisWithFullConfig allows custom Redis and circuit breaker config
func SetupRedisWithFullConfig(t *testing.T, redisCfg config.RedisConfig, cbConfig config.CircuitBreakerConfig) *hotstate.RedisHotState {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }

    // Use default TTL config
    defaultCfg := config.DefaultConfig()
    ttlConfig := defaultCfg.TTL

    return setupRedisInternal(t, redisCfg, cbConfig, ttlConfig)
}

// setupRedisInternal is the internal implementation
func setupRedisInternal(t *testing.T, redisCfg config.RedisConfig, cbConfig config.CircuitBreakerConfig, ttlConfig config.TTLConfig) *hotstate.RedisHotState {
    hs, err := hotstate.NewRedisHotState(redisCfg, cbConfig, ttlConfig)
    if err != nil {
        t.Fatalf("failed to connect to redis: %v", err)
    }

    // Flush test DB at start to ensure clean state
    ctx := context.Background()
    hs.Client().FlushDB(ctx)

    // Flush test DB on cleanup
    t.Cleanup(func() {
        ctx := context.Background()
        hs.Client().FlushDB(ctx)
        hs.Close()
    })

    return hs
}
