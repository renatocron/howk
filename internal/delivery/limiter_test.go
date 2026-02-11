package delivery

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/config"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	return mr, client
}

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{
			name:     "full URL with path",
			endpoint: "https://api.stripe.com/v1/webhooks",
			want:     "api.stripe.com",
		},
		{
			name:     "full URL with port",
			endpoint: "https://api.stripe.com:8443/webhooks",
			want:     "api.stripe.com:8443",
		},
		{
			name:     "just hostname",
			endpoint: "api.stripe.com",
			want:     "api.stripe.com",
		},
		{
			name:     "hostname with path",
			endpoint: "hooks.slack.com/services/T123/B456",
			want:     "hooks.slack.com",
		},
		{
			name:     "localhost with port",
			endpoint: "http://localhost:8080/webhook",
			want:     "localhost:8080",
		},
		{
			name:     "invalid URL falls back",
			endpoint: "not-a-valid-url",
			want:     "not-a-valid-url",
		},
		{
			name:     "empty string",
			endpoint: "",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractDomain(tt.endpoint)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRedisDomainLimiter_TryAcquire_Success(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 2,
		InflightTTL:          time.Minute,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// First acquire should succeed
	acquired, err := limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Second acquire should succeed
	acquired, err = limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Third acquire should fail (at limit)
	acquired, err = limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, acquired)

	// Different domain should succeed
	acquired, err = limiter.TryAcquire(ctx, "https://hooks.slack.com/webhook")
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestRedisDomainLimiter_TryAcquire_WithOverrides(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 2,
		DomainOverrides: map[string]int{
			"api.stripe.com": 5,
		},
		InflightTTL: time.Minute,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()

	// Should be able to acquire 5 for stripe (override)
	for i := 0; i < 5; i++ {
		acquired, err := limiter.TryAcquire(ctx, "https://api.stripe.com/webhooks")
		require.NoError(t, err)
		assert.True(t, acquired)
	}

	// 6th should fail
	acquired, err := limiter.TryAcquire(ctx, "https://api.stripe.com/webhooks")
	require.NoError(t, err)
	assert.False(t, acquired)

	// Other domain should still use default (2)
	for i := 0; i < 2; i++ {
		acquired, err := limiter.TryAcquire(ctx, "https://other.com/webhook")
		require.NoError(t, err)
		assert.True(t, acquired)
	}

	// 3rd for other should fail
	acquired, err = limiter.TryAcquire(ctx, "https://other.com/webhook")
	require.NoError(t, err)
	assert.False(t, acquired)
}

func TestRedisDomainLimiter_Release(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 1,
		InflightTTL:          time.Minute,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// Acquire
	acquired, err := limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Second acquire should fail
	acquired, err = limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, acquired)

	// Release
	err = limiter.Release(ctx, endpoint)
	require.NoError(t, err)

	// Now acquire should succeed
	acquired, err = limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, acquired)
}

func TestRedisDomainLimiter_FailOpen(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()

	// Close client to simulate Redis failure
	client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 2,
		InflightTTL:          time.Minute,
	}

	// Create new client pointing to closed server
	client = redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})
	mr.Close() // Close server too

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// Should fail open (allow) when Redis is down
	acquired, err := limiter.TryAcquire(ctx, endpoint)
	assert.NoError(t, err)
	assert.True(t, acquired)
}

func TestRedisDomainLimiter_Release_NeverBelowZero(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 2,
		InflightTTL:          time.Minute,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// Release without any acquire should not go below zero
	err := limiter.Release(ctx, endpoint)
	require.NoError(t, err)

	// Key should not exist or be 0
	key := domainConcurrencyPrefix + "api.stripe.com"
	val, err := client.Get(ctx, key).Result()
	// Key might not exist (redis.Nil) or be "0"
	if err == nil {
		assert.Equal(t, "0", val)
	}
}

func TestNoopDomainLimiter(t *testing.T) {
	limiter := NewNoopDomainLimiter()
	ctx := context.Background()

	// Always allows
	acquired, err := limiter.TryAcquire(ctx, "https://any.endpoint.com")
	assert.NoError(t, err)
	assert.True(t, acquired)

	// Multiple times
	for i := 0; i < 10; i++ {
		acquired, err = limiter.TryAcquire(ctx, "https://any.endpoint.com")
		assert.NoError(t, err)
		assert.True(t, acquired)
	}

	// Release is no-op
	err = limiter.Release(ctx, "https://any.endpoint.com")
	assert.NoError(t, err)
}

func TestRedisDomainLimiter_TTL(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 2,
		InflightTTL:          5 * time.Second,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// Acquire
	acquired, err := limiter.TryAcquire(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, acquired)

	// Check TTL is set
	key := domainConcurrencyPrefix + "api.stripe.com"
	ttl, err := client.TTL(ctx, key).Result()
	require.NoError(t, err)
	assert.True(t, ttl > 0 && ttl <= 5*time.Second)
}

func TestRedisDomainLimiter_ConcurrentAccess(t *testing.T) {
	mr, client := setupTestRedis(t)
	defer mr.Close()
	defer client.Close()

	cfg := config.ConcurrencyConfig{
		MaxInflightPerDomain: 10,
		InflightTTL:          time.Minute,
	}

	limiter := NewRedisDomainLimiter(client, cfg)
	ctx := context.Background()
	endpoint := "https://api.stripe.com/webhooks"

	// Run concurrent acquires
	done := make(chan int, 20)
	for i := 0; i < 20; i++ {
		go func() {
			acquired, _ := limiter.TryAcquire(ctx, endpoint)
			if acquired {
				done <- 1
			} else {
				done <- 0
			}
		}()
	}

	// Count successful acquires
	successCount := 0
	for i := 0; i < 20; i++ {
		successCount += <-done
	}

	// Should have exactly 10 successful acquires
	assert.Equal(t, 10, successCount)

	// Check counter in Redis
	key := domainConcurrencyPrefix + "api.stripe.com"
	val, err := client.Get(ctx, key).Int64()
	require.NoError(t, err)
	assert.Equal(t, int64(10), val)
}
