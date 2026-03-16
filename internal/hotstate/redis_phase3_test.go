//go:build !integration

package hotstate

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/domain"
)

// =============================================================================
// Step 3.1: GetRetryData / EnsureRetryData round-trip
// =============================================================================

func TestRetryData_EnsureAndGet_RoundTrip(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhook := &domain.Webhook{
		ID:           domain.WebhookID("wh-roundtrip-01"),
		ConfigID:     domain.ConfigID("cfg-001"),
		Endpoint:     "https://example.com/hook",
		EndpointHash: domain.EndpointHash("aabbccdd"),
		Payload:      json.RawMessage(`{"event":"order.created","order_id":42}`),
		Headers:      map[string]string{"X-Custom": "header-value"},
		Attempt:      3,
		MaxAttempts:  10,
		CreatedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ScheduledAt:  time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC),
		ScriptHash:   "deadbeef01",
	}
	ttl := 7 * 24 * time.Hour

	// Store via EnsureRetryData
	err := hs.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)

	// Retrieve via GetRetryData and verify every field
	got, err := hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, webhook.ID, got.ID)
	assert.Equal(t, webhook.ConfigID, got.ConfigID)
	assert.Equal(t, webhook.Endpoint, got.Endpoint)
	assert.Equal(t, webhook.EndpointHash, got.EndpointHash)
	assert.Equal(t, []byte(webhook.Payload), []byte(got.Payload))
	assert.Equal(t, webhook.Headers["X-Custom"], got.Headers["X-Custom"])
	assert.Equal(t, webhook.Attempt, got.Attempt)
	assert.Equal(t, webhook.MaxAttempts, got.MaxAttempts)
	assert.True(t, webhook.CreatedAt.Equal(got.CreatedAt), "CreatedAt must match")
	assert.True(t, webhook.ScheduledAt.Equal(got.ScheduledAt), "ScheduledAt must match")
	assert.Equal(t, webhook.ScriptHash, got.ScriptHash)
}

func TestRetryData_EnsureRetryData_TwiceOnlyRefreshesTTL(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-ttl-refresh"),
		ConfigID: domain.ConfigID("cfg-002"),
		Endpoint: "https://example.com/hook2",
		Payload:  json.RawMessage(`{"x":1}`),
	}
	ttl := 24 * time.Hour

	// First call: key missing, full compress+store
	err := hs.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)

	key := retryDataPrefix + string(webhook.ID)
	firstVal, ferr := s.Get(key)
	require.NoError(t, ferr, "key must exist after first EnsureRetryData")

	// Advance time and call again — key exists, only TTL refresh
	s.FastForward(1 * time.Hour)

	webhook.Payload = json.RawMessage(`{"x":99}`) // mutate payload — second call must NOT overwrite
	err = hs.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)

	secondVal, serr := s.Get(key)
	require.NoError(t, serr)
	// Stored bytes are identical — no re-compression occurred
	assert.Equal(t, firstVal, secondVal, "second EnsureRetryData must not overwrite existing data")

	// TTL must be refreshed (close to full ttl again)
	remaining := s.TTL(key)
	assert.True(t, remaining > 23*time.Hour, "TTL should be refreshed close to full duration, got %v", remaining)
}

func TestRetryData_DeleteRetryData_RemovesKey(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-delete-test"),
		ConfigID: domain.ConfigID("cfg-003"),
		Endpoint: "https://example.com/del",
		Payload:  json.RawMessage(`{}`),
	}

	err := hs.EnsureRetryData(ctx, webhook, time.Hour)
	require.NoError(t, err)

	// Verify it exists
	_, err = hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)

	// Delete
	err = hs.DeleteRetryData(ctx, webhook.ID)
	require.NoError(t, err)

	// Must be gone from miniredis
	key := retryDataPrefix + string(webhook.ID)
	_, getErr := s.Get(key)
	assert.Error(t, getErr, "key must not exist after DeleteRetryData")
}

func TestRetryData_GetRetryData_NotFound(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	_, err := hs.GetRetryData(ctx, domain.WebhookID("nonexistent-wh"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent-wh")
}

func TestRetryData_EnsureRetryData_TTLIsSet(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-ttl-set"),
		ConfigID: domain.ConfigID("cfg-004"),
		Endpoint: "https://example.com/hook3",
		Payload:  json.RawMessage(`{"ok":true}`),
	}
	ttl := 48 * time.Hour

	err := hs.EnsureRetryData(ctx, webhook, ttl)
	require.NoError(t, err)

	key := retryDataPrefix + string(webhook.ID)
	remaining := s.TTL(key)
	// TTL must be positive and close to the requested duration
	assert.True(t, remaining > 47*time.Hour && remaining <= 48*time.Hour,
		"TTL should be ~48h, got %v", remaining)
}

// =============================================================================
// Step 3.2: ShouldAllow circuit breaker states
// =============================================================================

func TestShouldAllow_ClosedCircuit_AllowsRequest(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-closed")
	cb := hs.CircuitBreaker()

	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed, "closed circuit must allow requests")
	assert.False(t, isProbe, "closed circuit must not mark as probe")
}

func TestShouldAllow_OpenCircuit_DeniesRequest(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-open-deny")
	// Store an open circuit state that was changed just now (recovery timeout not expired)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitOpen,
		Failures:       5,
		StateChangedAt: time.Now(), // just opened — recovery timeout NOT expired
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, allowed, "open circuit must deny requests")
	assert.False(t, isProbe)
}

func TestShouldAllow_OpenCircuit_RecoveryExpired_TransitionsToHalfOpen(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-open-recovery")
	// State changed far in the past (beyond RecoveryTimeout)
	stateChangedAt := time.Now().Add(-2 * defaultCircuitConfig.RecoveryTimeout)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitOpen,
		Failures:       5,
		StateChangedAt: stateChangedAt,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	// First call after recovery timeout: transitions open → half-open, allows probe
	assert.True(t, allowed, "after recovery timeout, one probe must be allowed")
	assert.True(t, isProbe, "the allowed request must be marked as probe")
}

func TestShouldAllow_OpenCircuit_RecoveryExpired_SecondCallDenied(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-open-second")
	stateChangedAt := time.Now().Add(-2 * defaultCircuitConfig.RecoveryTimeout)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitOpen,
		Failures:       5,
		StateChangedAt: stateChangedAt,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()

	// First call: probe allowed
	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, isProbe)

	// Second call immediately after: probe lock is set, so denied
	allowed2, isProbe2, err2 := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err2)
	assert.False(t, allowed2, "second immediate call must be denied while probe lock is held")
	assert.False(t, isProbe2)
}

func TestShouldAllow_HalfOpenCircuit_ProbeDue_AllowsProbe(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-halfopen-probe")
	// Half-open, NextProbeAt is in the past
	pastProbe := time.Now().Add(-time.Minute)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitHalfOpen,
		Failures:       3,
		StateChangedAt: time.Now().Add(-6 * time.Minute),
		NextProbeAt:    &pastProbe,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed, "half-open with past probe time must allow probe")
	assert.True(t, isProbe)
}

func TestShouldAllow_HalfOpenCircuit_ProbeNotDue_Denies(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-halfopen-notdue")
	// Half-open, NextProbeAt is in the future
	futureProbe := time.Now().Add(10 * time.Minute)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitHalfOpen,
		Failures:       3,
		StateChangedAt: time.Now().Add(-2 * time.Minute),
		NextProbeAt:    &futureProbe,
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	allowed, isProbe, err := cb.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, allowed, "half-open with future probe time must deny")
	assert.False(t, isProbe)
}

// =============================================================================
// Step 3.3: RecordSuccess and RecordFailure state transitions
// =============================================================================

func TestRecordSuccess_ClosedCircuit_StaysClosed(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-success-closed")

	cb := hs.CircuitBreaker()
	result, err := cb.RecordSuccess(ctx, endpoint)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.CircuitClosed, result.State, "closed circuit stays closed on success")
	assert.Equal(t, 0, result.Failures, "failures reset on success")
}

func TestRecordFailure_ClosedCircuit_BelowThreshold_StaysClosed(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-fail-below")

	cb := hs.CircuitBreaker()
	// threshold is 3; record 2 failures within the window
	var result *domain.CircuitBreaker
	var err error
	for i := 0; i < defaultCircuitConfig.FailureThreshold-1; i++ {
		result, err = cb.RecordFailure(ctx, endpoint)
		require.NoError(t, err)
	}
	assert.Equal(t, domain.CircuitClosed, result.State, "circuit stays closed below threshold")
}

func TestRecordFailure_ClosedCircuit_ReachesThreshold_OpensCircuit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-fail-opens")

	cb := hs.CircuitBreaker()
	var result *domain.CircuitBreaker
	var err error
	// Record exactly FailureThreshold (3) failures within the failure window
	for i := 0; i < defaultCircuitConfig.FailureThreshold; i++ {
		result, err = cb.RecordFailure(ctx, endpoint)
		require.NoError(t, err)
	}
	assert.Equal(t, domain.CircuitOpen, result.State, "circuit must open after reaching threshold")
	assert.True(t, result.Failures >= defaultCircuitConfig.FailureThreshold)
}

func TestRecordSuccess_HalfOpen_ReachesSuccessThreshold_ClosesCirucit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-halfopen-closes")
	// Pre-store half-open state
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitHalfOpen,
		Failures:       3,
		Successes:      0,
		StateChangedAt: time.Now().Add(-3 * time.Minute),
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	// SuccessThreshold is 2 — record 2 successes
	var result *domain.CircuitBreaker
	var err error
	for i := 0; i < defaultCircuitConfig.SuccessThreshold; i++ {
		result, err = cb.RecordSuccess(ctx, endpoint)
		require.NoError(t, err)
	}
	assert.Equal(t, domain.CircuitClosed, result.State, "half-open closes after success threshold")
	assert.Equal(t, 0, result.Failures)
	assert.Equal(t, 0, result.Successes)
	assert.Nil(t, result.NextProbeAt)
}

func TestRecordFailure_HalfOpen_ReOpensCircuit(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-halfopen-reopens")
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitHalfOpen,
		Failures:       3,
		Successes:      1,
		StateChangedAt: time.Now().Add(-3 * time.Minute),
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	result, err := cb.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitOpen, result.State, "failure in half-open reopens circuit")
	assert.Equal(t, 0, result.Successes)
	assert.Nil(t, result.NextProbeAt)
}

func TestRecordFailure_OutsideWindow_ResetsCounter(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-fail-outside-window")

	// Pre-store a closed circuit with 2 failures but LastFailureAt is outside the window
	lastFailureAt := time.Now().Add(-2 * defaultCircuitConfig.FailureWindow)
	state := domain.CircuitBreaker{
		EndpointHash:   endpoint,
		State:          domain.CircuitClosed,
		Failures:       defaultCircuitConfig.FailureThreshold - 1,
		LastFailureAt:  &lastFailureAt,
		StateChangedAt: time.Now().Add(-10 * time.Minute),
	}
	data, _ := json.Marshal(state)
	require.NoError(t, s.Set(circuitStatePrefix+string(endpoint), string(data)))

	cb := hs.CircuitBreaker()
	// This failure is outside the window; counter must reset to 1
	result, err := cb.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitClosed, result.State, "outside window — counter resets, circuit stays closed")
	assert.Equal(t, 1, result.Failures, "counter must reset to 1")
}

// =============================================================================
// Step 3.4: IncrStats / GetStats multi-bucket and AddToHLL
// =============================================================================

func TestIncrStats_MultipleHourlyBuckets_AggregatesCorrectly(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	baseHour := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	hour1 := baseHour
	hour2 := baseHour.Add(time.Hour)
	hour3 := baseHour.Add(2 * time.Hour)

	bucket1 := hour1.Format("2006010215")
	bucket2 := hour2.Format("2006010215")
	bucket3 := hour3.Format("2006010215")

	require.NoError(t, hs.IncrStats(ctx, bucket1, map[string]int64{"enqueued": 10, "delivered": 8}))
	require.NoError(t, hs.IncrStats(ctx, bucket2, map[string]int64{"enqueued": 20, "delivered": 15, "failed": 5}))
	require.NoError(t, hs.IncrStats(ctx, bucket3, map[string]int64{"enqueued": 5, "exhausted": 2}))

	// Query the full 3-hour span
	stats, err := hs.GetStats(ctx, hour1, hour3)
	require.NoError(t, err)
	assert.Equal(t, int64(35), stats.Enqueued)
	assert.Equal(t, int64(23), stats.Delivered)
	assert.Equal(t, int64(5), stats.Failed)
	assert.Equal(t, int64(2), stats.Exhausted)
}

func TestGetStats_EmptyRange_ReturnsZeroes(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	baseHour := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	stats, err := hs.GetStats(ctx, baseHour, baseHour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), stats.Enqueued)
	assert.Equal(t, int64(0), stats.Delivered)
	assert.Equal(t, int64(0), stats.UniqueEndpoints)
}

func TestAddToHLL_DeduplicatesValues(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	now := time.Now().Truncate(time.Hour)
	bucket := now.Format("2006010215")
	hllKey := "endpoints:" + bucket

	// Add the same value multiple times and distinct values
	require.NoError(t, hs.AddToHLL(ctx, hllKey, "ep1", "ep2", "ep1"))
	require.NoError(t, hs.AddToHLL(ctx, hllKey, "ep1", "ep3"))

	stats, err := hs.GetStats(ctx, now, now)
	require.NoError(t, err)
	// ep1, ep2, ep3 — 3 unique values (miniredis HLL is exact)
	assert.Equal(t, int64(3), stats.UniqueEndpoints)
}

func TestAddToHLL_SetsExpiry(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	now := time.Now().Truncate(time.Hour)
	bucket := now.Format("2006010215")

	require.NoError(t, hs.AddToHLL(ctx, "endpoints:"+bucket, "ep1"))

	key := hllPrefix + "endpoints:" + bucket
	ttl := s.TTL(key)
	assert.True(t, ttl > 0, "HLL key must have a TTL set, got %v", ttl)
}

// =============================================================================
// Step 3.5: CheckCanary / SetCanary / WaitForCanary / DelCanary
// =============================================================================

func TestCanary_SetAndCheck(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Canary must not exist initially
	exists, err := hs.CheckCanary(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "canary must not exist before SetCanary")

	// Set canary
	require.NoError(t, hs.SetCanary(ctx))

	// Now it must exist
	exists, err = hs.CheckCanary(ctx)
	require.NoError(t, err)
	assert.True(t, exists, "canary must exist after SetCanary")
}

func TestCanary_DelCanary_RemovesKey(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	require.NoError(t, hs.SetCanary(ctx))

	exists, _ := hs.CheckCanary(ctx)
	require.True(t, exists)

	require.NoError(t, hs.DelCanary(ctx))

	exists, err := hs.CheckCanary(ctx)
	require.NoError(t, err)
	assert.False(t, exists, "canary must be gone after DelCanary")
}

func TestCanary_WaitForCanary_ReturnsTrueWhenSet(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Pre-set canary so WaitForCanary returns immediately
	require.NoError(t, hs.SetCanary(ctx))

	found := hs.WaitForCanary(ctx, 2*time.Second)
	assert.True(t, found, "WaitForCanary must return true when canary is already set")
}

func TestCanary_WaitForCanary_TimesOutWhenNotSet(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Canary is never set
	start := time.Now()
	found := hs.WaitForCanary(ctx, 600*time.Millisecond)
	elapsed := time.Since(start)

	assert.False(t, found, "WaitForCanary must return false when canary never appears")
	assert.True(t, elapsed >= 600*time.Millisecond, "must wait at least the timeout duration")
}

// =============================================================================
// Step 3.6: GetEpoch / SetEpoch and GetRetryQueueSize
// =============================================================================

func TestSetEpoch_GetEpoch_RoundTrip(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// No epoch set yet
	got, err := hs.GetEpoch(ctx)
	require.NoError(t, err)
	assert.Nil(t, got, "GetEpoch must return nil when no epoch is stored")

	epoch := &domain.SystemEpoch{
		Epoch:            42,
		ReconcilerHost:   "reconciler-pod-1",
		MessagesReplayed: 1234,
		CompletedAt:      time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	require.NoError(t, hs.SetEpoch(ctx, epoch))

	got, err = hs.GetEpoch(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, epoch.Epoch, got.Epoch)
	assert.Equal(t, epoch.ReconcilerHost, got.ReconcilerHost)
	assert.Equal(t, epoch.MessagesReplayed, got.MessagesReplayed)
	assert.True(t, epoch.CompletedAt.Equal(got.CompletedAt), "CompletedAt must match")
}

func TestSetEpoch_Overwrite_UpdatesValue(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	first := &domain.SystemEpoch{Epoch: 1, ReconcilerHost: "host-a", MessagesReplayed: 100}
	require.NoError(t, hs.SetEpoch(ctx, first))

	second := &domain.SystemEpoch{Epoch: 2, ReconcilerHost: "host-b", MessagesReplayed: 200}
	require.NoError(t, hs.SetEpoch(ctx, second))

	got, err := hs.GetEpoch(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Epoch)
	assert.Equal(t, "host-b", got.ReconcilerHost)
}

func TestGetRetryQueueSize_Empty(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	size, err := hs.GetRetryQueueSize(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), size, "empty queue must return 0")
}

func TestGetRetryQueueSize_Populated(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Schedule 4 retries at various future times
	base := time.Now()
	for i := range 4 {
		webhookID := domain.WebhookID(fmt.Sprintf("wh-qsize-%d", i))
		err := hs.ScheduleRetry(ctx, webhookID, i, base.Add(time.Duration(i)*time.Minute), "test")
		require.NoError(t, err)
	}

	size, err := hs.GetRetryQueueSize(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(4), size)
}

// =============================================================================
// Step 3.7: AckRetry and PopAndLockRetries with visibility timeout
// =============================================================================

func TestPopAndLockRetries_VisibilityTimeout_PushesScoreToFuture(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	// Schedule 3 due retries (past time)
	past := time.Now().Add(-30 * time.Second)
	for i := range 3 {
		webhookID := domain.WebhookID(fmt.Sprintf("wh-vis-%d", i))
		err := hs.ScheduleRetry(ctx, webhookID, i, past, "vis-test")
		require.NoError(t, err)
	}

	lockDuration := 60 * time.Second
	refs, err := hs.PopAndLockRetries(ctx, 10, lockDuration)
	require.NoError(t, err)
	require.Len(t, refs, 3, "all 3 due retries must be returned")

	// Verify that scores were pushed to the future (visibility timeout)
	// Items are still in the ZSET but with a future score — they won't be popped again
	refsAgain, err := hs.PopAndLockRetries(ctx, 10, lockDuration)
	require.NoError(t, err)
	assert.Empty(t, refsAgain, "locked retries must not be re-popped before visibility timeout expires")
}

func TestAckRetry_RemovesFromZSET(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	past := time.Now().Add(-10 * time.Second)
	webhookID := domain.WebhookID("wh-ack-test")
	attempt := 1
	require.NoError(t, hs.ScheduleRetry(ctx, webhookID, attempt, past, "ack"))

	// Pop and lock
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 1)

	reference := refs[0]

	// Ack
	err = hs.AckRetry(ctx, reference)
	require.NoError(t, err)

	// ZSET must be empty
	size, err := hs.GetRetryQueueSize(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), size, "ZSET must be empty after AckRetry")
}

func TestAckRetry_RemovesMetadata(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	past := time.Now().Add(-10 * time.Second)
	webhookID := domain.WebhookID("wh-ack-meta")
	attempt := 2
	require.NoError(t, hs.ScheduleRetry(ctx, webhookID, attempt, past, "meta-test"))

	reference := fmt.Sprintf("%s:%d", webhookID, attempt)
	metaKey := retryMetaPrefix + reference

	// Verify metadata key exists
	_, err := s.Get(metaKey)
	require.NoError(t, err, "meta key must exist after ScheduleRetry")

	// Ack removes metadata too
	require.NoError(t, hs.AckRetry(ctx, reference))

	_, err = s.Get(metaKey)
	assert.Error(t, err, "meta key must be removed after AckRetry")
}

func TestAckRetry_DoesNotDeleteRetryData(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	past := time.Now().Add(-5 * time.Second)
	webhook := &domain.Webhook{
		ID:       domain.WebhookID("wh-ack-data-persist"),
		ConfigID: domain.ConfigID("cfg-ack"),
		Endpoint: "https://example.com/ack",
		Payload:  json.RawMessage(`{"persist":true}`),
	}

	// Store data + schedule retry
	require.NoError(t, hs.EnsureRetryData(ctx, webhook, time.Hour))
	require.NoError(t, hs.ScheduleRetry(ctx, webhook.ID, 1, past, "data-persist"))

	// Pop and ack
	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 1)

	require.NoError(t, hs.AckRetry(ctx, refs[0]))

	// Data must still be retrievable (AckRetry must NOT delete it)
	got, err := hs.GetRetryData(ctx, webhook.ID)
	require.NoError(t, err)
	assert.Equal(t, webhook.ID, got.ID, "retry data must survive AckRetry")
}

func TestPopAndLockRetries_OnlyDue_SkipsFuture(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	past := time.Now().Add(-20 * time.Second)
	future := time.Now().Add(1 * time.Hour)

	// Schedule one past (due) and one future (not due)
	require.NoError(t, hs.ScheduleRetry(ctx, "wh-due", 1, past, "due"))
	require.NoError(t, hs.ScheduleRetry(ctx, "wh-future", 1, future, "future"))

	refs, err := hs.PopAndLockRetries(ctx, 10, 30*time.Second)
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, "wh-due:1", refs[0], "only the due retry must be returned")
}

// =============================================================================
// Step 3.8: IncrInflight / DecrInflight additional coverage
// =============================================================================

func TestIncrInflight_FirstIncrement_SetsTTL(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-ttl-first")
	ttl := 5 * time.Minute

	count, err := hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	key := concurrencyPrefix + string(endpoint)
	remaining := s.TTL(key)
	assert.True(t, remaining > 0 && remaining <= ttl,
		"TTL must be set on first IncrInflight, got %v", remaining)
}

func TestDecrInflight_NeverGoesBelowZero(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-no-negative")
	ttl := time.Minute

	// Bring counter to 1 then back to 0
	_, err := hs.IncrInflight(ctx, endpoint, ttl)
	require.NoError(t, err)
	require.NoError(t, hs.DecrInflight(ctx, endpoint))

	key := concurrencyPrefix + string(endpoint)
	val, _ := s.Get(key)
	assert.Equal(t, "0", val)

	// Multiple extra decrements must floor at 0
	for range 3 {
		require.NoError(t, hs.DecrInflight(ctx, endpoint))
	}

	val, _ = s.Get(key)
	assert.Equal(t, "0", val, "counter must floor at 0 after excess decrements")
}

func TestIncrInflight_ReturnsNewCount(t *testing.T) {
	ctx := context.Background()
	hs, s := setupMiniredis(t)
	defer s.Close()

	endpoint := domain.EndpointHash("ep-count-return")
	ttl := time.Minute

	for expected := int64(1); expected <= 5; expected++ {
		count, err := hs.IncrInflight(ctx, endpoint, ttl)
		require.NoError(t, err)
		assert.Equal(t, expected, count, "IncrInflight must return the new counter value")
	}
}

func TestDecrInflight_OnNonExistentKey_NoError(t *testing.T) {
	ctx := context.Background()
	hs, _ := setupMiniredis(t)

	// Decrement on a key that was never incremented — Lua script handles GET returning nil
	err := hs.DecrInflight(ctx, domain.EndpointHash("ep-never-existed"))
	assert.NoError(t, err, "DecrInflight on non-existent key must not error")
}
