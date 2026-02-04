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

func TestScheduleRetry_PopAndLockRetries(t *testing.T) {
	env := testutil.NewIsolatedEnv(t)
	hs := env.HotState
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
	env := testutil.NewIsolatedEnv(t)
	hs := env.HotState
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
	// Use env.Redis for direct Redis access with prefix applied
	count, err := env.Redis.ZCount(ctx, "retries", "-inf", "+inf").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// Cleanup
	hs.DeleteRetryData(ctx, whPast.ID)
	hs.DeleteRetryData(ctx, whFuture.ID)
}

func TestRetryDataLifecycle(t *testing.T) {
	env := testutil.NewIsolatedEnv(t)
	hs := env.HotState
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

func TestCheckAndSetProcessed_TTL(t *testing.T) {
	env := testutil.NewIsolatedEnv(t)
	hs := env.HotState
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
