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

// SetupRedisWithConfig allows custom config
func SetupRedisWithConfig(t *testing.T, cfg config.RedisConfig) *hotstate.RedisHotState {
    if testing.Short() {
        t.Skip("skipping integration test in short mode")
    }

    hs, err := hotstate.NewRedisHotState(cfg)
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
