//go:build integration

package testutil

import (
    "context"
    "testing"
    "github.com/howk/howk/internal/config"
    "github.com/howk/howk/internal/hotstate"
)

// SetupRedis connects to localhost:6379, flushes DB on cleanup
func SetupRedis(t *testing.T) *hotstate.RedisHotState {
	cfg := config.RedisConfig{
		Addr: "localhost:6379",
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

    // Flush test DB on cleanup
    t.Cleanup(func() {
        ctx := context.Background()
        hs.Client().FlushDB(ctx)
        hs.Close()
    })

    return hs
}
