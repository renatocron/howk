//go:build integration

package hotstate_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetStatus_GetStatus(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

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

func TestScheduleRetry_PopDueRetries(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhook := testutil.NewTestWebhook("http://example.com/retry")
	retryMsg := &hotstate.RetryMessage{
		Webhook:     webhook,
		ScheduledAt: time.Now().Add(100 * time.Millisecond),
		Reason:      "test",
	}

	err := hs.ScheduleRetry(ctx, retryMsg)
	require.NoError(t, err)

	// Pop immediately - should get nothing
	retries, err := hs.PopDueRetries(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, retries)

	// Wait for retry to be due
	time.Sleep(150 * time.Millisecond)

	// Pop again - should get the webhook
	retries, err = hs.PopDueRetries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, retries, 1)
	assert.Equal(t, webhook.ID, retries[0].Webhook.ID)
	assert.Equal(t, retryMsg.Reason, retries[0].Reason)

	// Pop again - should be empty
	retries, err = hs.PopDueRetries(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, retries)
}

func TestPopDueRetries_OnlyDue(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	// Schedule one in the past, one in the future
	whPast := testutil.NewTestWebhook("http://example.com/past")
	pastMsg := &hotstate.RetryMessage{
		Webhook:     whPast,
		ScheduledAt: time.Now().Add(-100 * time.Millisecond),
	}
	err := hs.ScheduleRetry(ctx, pastMsg)
	require.NoError(t, err)

	whFuture := testutil.NewTestWebhook("http://example.com/future")
	futureMsg := &hotstate.RetryMessage{
		Webhook:     whFuture,
		ScheduledAt: time.Now().Add(1 * time.Hour),
	}
	err = hs.ScheduleRetry(ctx, futureMsg)
	require.NoError(t, err)

	retries, err := hs.PopDueRetries(ctx, 10)
	require.NoError(t, err)
	require.Len(t, retries, 1)
	assert.Equal(t, whPast.ID, retries[0].Webhook.ID)

	// The future one should still be there
	count, err := hs.Client().ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestPopDueRetries_BatchSize(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		webhook := testutil.NewTestWebhook("http://example.com/batch")
		retryMsg := &hotstate.RetryMessage{
			Webhook:     webhook,
			ScheduledAt: time.Now().Add(-10 * time.Second), // All due
		}
		err := hs.ScheduleRetry(ctx, retryMsg)
		require.NoError(t, err)
	}

	retries, err := hs.PopDueRetries(ctx, 3)
	require.NoError(t, err)
	require.Len(t, retries, 3)

	count, err := hs.Client().ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count) // 2 remaining
}

func TestIncrStats_GetStats(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

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

func TestAddToHLL_GetStats(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

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

func TestCheckAndSetProcessed_First(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhookID := domain.WebhookID("processed-webhook-first")
	attempt := 1
	ttl := 1 * time.Hour

	set, err := hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.True(t, set) // Should be set successfully

	// Verify it exists in Redis
	val, err := hs.Client().Get(ctx, "processed:processed-webhook-first:1").Result()
	require.NoError(t, err)
	assert.Equal(t, "1", val)
}

func TestCheckAndSetProcessed_Duplicate(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

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

func TestCheckAndSetProcessed_TTL(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhookID := domain.WebhookID("processed-webhook-ttl")
	attempt := 1
	ttl := 10 * time.Millisecond

	set, err := hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.True(t, set)

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	// Should be able to set it again after expiration
	set, err = hs.CheckAndSetProcessed(ctx, webhookID, attempt, ttl)
	require.NoError(t, err)
	assert.True(t, set)
}
