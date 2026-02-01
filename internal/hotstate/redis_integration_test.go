//go:build integration

package hotstate_test

import (
	"context"
	"testing"
	"time"

	"github.com/howk/howk/internal/domain"
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

func TestScheduleRetry_PopAndLockRetries(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhook := testutil.NewTestWebhook("http://example.com/retry")
	webhook.Attempt = 1

	// Store retry data first
	err := hs.EnsureRetryData(ctx, webhook, 7*24*time.Hour)
	require.NoError(t, err)

	// Schedule retry reference
	err = hs.ScheduleRetry(ctx, webhook.ID, webhook.Attempt, time.Now().Add(2*time.Second), "test")
	require.NoError(t, err)

	// Pop immediately - should get nothing (not due yet)
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, refs)

	// Wait for retry to be due
	time.Sleep(3 * time.Second)

	// Pop again - should get the reference
	refs, err = hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, string(webhook.ID)+":1", refs[0])

	// Fetch data by webhookID
	fetchedWebhook, err := hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, fetchedWebhook.ID)

	// Ack the retry (removes reference but not data)
	err = hs.AckRetry(ctx, refs[0])
	require.NoError(t, err)

	// Verify reference is gone
	refs, err = hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	assert.Empty(t, refs)

	// Cleanup data
	err = hs.DeleteRetryData(ctx, webhook.ID)
	require.NoError(t, err)
}

func TestPopAndLockRetries_OnlyDue(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	// Schedule one in the past (due), one in the future (not due)
	whPast := testutil.NewTestWebhook("http://example.com/past")
	err := hs.EnsureRetryData(ctx, whPast, 7*24*time.Hour)
	require.NoError(t, err)
	err = hs.ScheduleRetry(ctx, whPast.ID, 1, time.Now().Add(-100*time.Millisecond), "past")
	require.NoError(t, err)

	whFuture := testutil.NewTestWebhook("http://example.com/future")
	err = hs.EnsureRetryData(ctx, whFuture, 7*24*time.Hour)
	require.NoError(t, err)
	err = hs.ScheduleRetry(ctx, whFuture.ID, 1, time.Now().Add(1*time.Hour), "future")
	require.NoError(t, err)

	// Pop due retries
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Contains(t, refs, string(whPast.ID)+":1")

	// Ack the popped retry
	err = hs.AckRetry(ctx, refs[0])
	require.NoError(t, err)

	// The future one should still be there (in the queue, locked score in future)
	count, err := hs.Client().ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Cleanup
	hs.DeleteRetryData(ctx, whPast.ID)
	hs.DeleteRetryData(ctx, whFuture.ID)
}

func TestPopAndLockRetries_BatchSize(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhooks := make([]*domain.Webhook, 5)
	for i := 0; i < 5; i++ {
		webhooks[i] = testutil.NewTestWebhook("http://example.com/batch")
		err := hs.EnsureRetryData(ctx, webhooks[i], 7*24*time.Hour)
		require.NoError(t, err)
		err = hs.ScheduleRetry(ctx, webhooks[i].ID, i, time.Now().Add(-10*time.Second), "batch") // All due
		require.NoError(t, err)
	}

	refs, err := hs.PopAndLockRetries(ctx, 3, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 3)

	// The remaining 2 should still be in the queue but with locked scores (in the future)
	// ZCount returns ALL items, including locked ones
	count, err := hs.Client().ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(5), count) // All 5 still in queue (3 locked, 2 pending)

	// Cleanup remaining data
	for _, wh := range webhooks {
		hs.DeleteRetryData(ctx, wh.ID)
	}

	// Clean up the retry queue
	hs.Client().Del(ctx, "retries")
}

func TestRetryDataLifecycle(t *testing.T) {
	hs := testutil.SetupRedis(t)
	ctx := context.Background()

	webhook := testutil.NewTestWebhook("http://example.com/lifecycle")

	// 1. Store data
	err := hs.EnsureRetryData(ctx, webhook, 7*24*time.Hour)
	require.NoError(t, err)

	// 2. Schedule multiple attempts
	for i := 1; i <= 3; i++ {
		err = hs.ScheduleRetry(ctx, webhook.ID, i, time.Now().Add(time.Duration(i)*time.Second), "attempt")
		require.NoError(t, err)
	}

	// 3. Verify data exists
	fetched, err := hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, fetched.ID)

	// 4. Ack all references
	for i := 1; i <= 3; i++ {
		ref := string(webhook.ID) + ":" + string(rune('0'+i))
		hs.AckRetry(ctx, ref)
	}

	// 5. Data should still exist
	fetched, err = hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)
	assert.NotNil(t, fetched)

	// 6. Delete data explicitly (terminal state)
	err = hs.DeleteRetryData(ctx, webhook.ID)
	require.NoError(t, err)

	// 7. Data should be gone
	_, err = hs.GetRetryData(ctx, webhook.ID)
	assert.Error(t, err)
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
