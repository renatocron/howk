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

// =============================================================================
// Unit Tests (converted from integration tests)
// =============================================================================

func TestSetStatus_GetStatus_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	status := &domain.WebhookStatus{
		WebhookID: "test-webhook-id",
		State:     domain.StatePending,
		Attempts:  0,
	}

	err := hs.SetStatus(ctx, status)
	require.NoError(t, err)

	gotStatus, err := hs.GetStatus(ctx, status.WebhookID)
	require.NoError(t, err)
	require.NotNil(t, gotStatus)
	assert.Equal(t, status.WebhookID, gotStatus.WebhookID)
	assert.Equal(t, status.State, gotStatus.State)
}

func TestIncrStats_GetStats_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	now := time.Now().Truncate(time.Hour)
	bucket := now.Format("2006010215") // hourly bucket

	// Increment some stats
	err := hs.IncrStats(ctx, bucket, map[string]int64{
		"enqueued":  10,
		"delivered": 5,
		"failed":    3,
	})
	require.NoError(t, err)

	err = hs.IncrStats(ctx, bucket, map[string]int64{
		"enqueued":  5,
		"delivered": 2,
		"exhausted": 1,
	})
	require.NoError(t, err)

	// Get stats for that hour
	stats, err := hs.GetStats(ctx, now, now)
	require.NoError(t, err)
	assert.Equal(t, int64(15), stats.Enqueued)
	assert.Equal(t, int64(7), stats.Delivered)
	assert.Equal(t, int64(3), stats.Failed)
	assert.Equal(t, int64(1), stats.Exhausted)

	// Get stats for a different hour - should be 0
	yesterday := now.Add(-24 * time.Hour)
	stats, err = hs.GetStats(ctx, yesterday, yesterday)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.Enqueued)
}

func TestAddToHLL_GetStats_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	now := time.Now().Truncate(time.Hour)
	bucket := now.Format("2006010215")

	err := hs.AddToHLL(ctx, "endpoints:"+bucket, "endpoint1", "endpoint2", "endpoint1")
	require.NoError(t, err)

	err = hs.AddToHLL(ctx, "endpoints:"+bucket, "endpoint3")
	require.NoError(t, err)

	stats, err := hs.GetStats(ctx, now, now)
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.UniqueEndpoints) // endpoint1, endpoint2, endpoint3
}

func TestCheckAndSetProcessed_First_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhookID := domain.WebhookID("processed-webhook-first")
	attempt := 1
	ttl := 1 * time.Hour

	set, err := hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.True(t, set) // Should be set successfully

	// Verify it exists in Redis
	key := "processed:processed-webhook-first:1"
	val, err := s.Get(key)
	require.NoError(t, err)
	assert.Equal(t, "1", val)
}

func TestCheckAndSetProcessed_Duplicate_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhookID := domain.WebhookID("processed-webhook-duplicate")
	attempt := 1
	ttl := 1 * time.Hour

	// First call should set it
	set, err := hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.True(t, set)

	// Second call should return false (already processed)
	set, err = hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.False(t, set)
}

func TestPopAndLockRetries_BatchSize_Unit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Schedule 5 retries all due immediately (past time)
	pastTime := time.Now().Add(-10 * time.Second)
	for i := 0; i < 5; i++ {
		webhookID := domain.WebhookID("wh_batch_" + string(rune('a'+i)))
		err := hs.ScheduleRetry(ctx, webhookID, i, pastTime, "batch")
		require.NoError(t, err)
	}

	// Pop only 3 (batch size limit)
	refs, err := hs.PopAndLockRetries(ctx, 3, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 3)

	// All 5 are still in queue (3 locked with future score, 2 still due)
	count, err := hs.rdb.ZCard(ctx, "retries").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(5), count)
}
