//go:build !integration

package hotstate

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

func setupMiniredis(t *testing.T) (*RedisHotState, *miniredis.Miniredis) {
	s := miniredis.RunT(t)

	rdb := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})

	cfg := config.RedisConfig{Addr: s.Addr()}
	cbCfg := defaultCircuitConfig
	ttlCfg := defaultTTLConfig

	hs := &RedisHotState{
		rdb:           rdb,
		config:        cfg,
		circuitConfig: cbCfg,
		ttlConfig:     ttlCfg,
	}

	return hs, s
}

func TestIncrInflight_Basic(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("test-endpoint")
	ttl := 2 * time.Minute

	// First increment should return 1
	count, err := hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Second increment should return 2
	count, err = hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// Third increment should return 3
	count, err = hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)

	// Verify the key exists with correct value
	key := concurrencyPrefix + string(endpoint)
	val, err := s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "3", val)
}

func TestIncrInflight_TTLRefresh(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("test-endpoint-ttl")
	ttl := 2 * time.Minute

	// First increment
	_, err := hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)

	// Get initial TTL
	key := concurrencyPrefix + string(endpoint)
	initialTTL := s.TTL(key)
	assert.True(t, initialTTL > 0 && initialTTL <= 2*time.Minute)

	// Fast forward almost to expiration
	s.FastForward(119 * time.Second)

	// Increment again - should refresh TTL
	_, err = hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)

	// TTL should be reset to full duration
	newTTL := s.TTL(key)
	assert.True(t, newTTL > 119*time.Second, "TTL should be refreshed after INCR")
}

func TestDecrInflight_Normal(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("test-endpoint-decr")
	ttl := 2 * time.Minute

	// Increment to 3
	for i := 0; i < 3; i++ {
		_, err := hs.IncrInflight(ctx, endpoint, ttl)
		require.NoError(t, err)
	}

	// Verify initial value
	key := concurrencyPrefix + string(endpoint)
	val, err := s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "3", val)

	// Decrement should work
	err = hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	val, err = s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "2", val)

	// Decrement again
	err = hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	val, err = s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "1", val)

	// Decrement to 0
	err = hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	val, err = s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "0", val)
}

func TestDecrInflight_FloorAtZero(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("test-endpoint-floor")

	// Decrement on non-existent key should not go below 0
	err := hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	// Key should not exist (or be 0)
	key := concurrencyPrefix + string(endpoint)
	_, err = s.Get(key)
	// miniredis returns error for non-existent key
	assert.Error(t, err)

	// Now create a key with value 1
	_, err = hs.IncrInflight(ctx, endpoint, time.Minute)
	require.NoError(t, err)

	// Decrement to 0
	err = hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	// Verify value is 0
	val, err := s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "0", val)

	// Decrement again - should stay at 0 due to Lua script floor
	err = hs.DecrInflight(ctx, endpoint)
	require.NoError(t, err)

	val, _ = s.Get(key)
	assert.Equal(t, "0", val, "Counter should floor at 0")

	// Multiple decrements should all floor at 0
	for i := 0; i < 5; i++ {
		err = hs.DecrInflight(ctx, endpoint)
		require.NoError(t, err)
	}

	val, _ = s.Get(key)
	assert.Equal(t, "0", val, "Counter should still be 0 after multiple decrements")
}

func TestIncrInflight_DifferentEndpoints(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint1 := domain.EndpointHash("endpoint-1")
	endpoint2 := domain.EndpointHash("endpoint-2")
	ttl := time.Minute

	// Increment each endpoint separately
	count1, err := hs.IncrInflight(ctx, endpoint1, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count1)

	count2, err := hs.IncrInflight(ctx, endpoint2, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count2)

	// Increment endpoint1 again
	count1, err = hs.IncrInflight(ctx, endpoint1, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count1)

	// Verify endpoint2 is still 1
	count2, err = hs.IncrInflight(ctx, endpoint2, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count2)

	// Verify keys are separate
	key1 := concurrencyPrefix + string(endpoint1)
	key2 := concurrencyPrefix + string(endpoint2)

	val1, err := s.Get(key1)
	require.NoError(t, err)
	assert.Equal(t, "2", val1)

	val2, err := s.Get(key2)
	require.NoError(t, err)
	assert.Equal(t, "2", val2)
}

func TestConcurrency_Integration(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("test-endpoint-integration")
	ttl := time.Minute

	// Simulate multiple workers incrementing
	for i := 0; i < 10; i++ {
		_, err := hs.IncrInflight(ctx, endpoint, ttl)
		require.NoError(t, err)
	}

	// Verify count is 10
	key := concurrencyPrefix + string(endpoint)
	val, err := s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "10", val)

	// Simulate workers completing (decrementing)
	for i := 0; i < 10; i++ {
		err := hs.DecrInflight(ctx, endpoint)
		require.NoError(t, err)
	}

	// Verify count is 0
	val, err = s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "0", val)

	// Extra decrements should floor at 0
	for i := 0; i < 5; i++ {
		err := hs.DecrInflight(ctx, endpoint)
		require.NoError(t, err)
	}

	val, err = s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "0", val)
}
