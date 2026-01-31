package circuit_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/howk/howk/internal/circuit"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupBreaker(t *testing.T) (*circuit.Breaker, *time.Time) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := config.CircuitBreakerConfig{
		FailureThreshold: 3,
		RecoveryTimeout:  100 * time.Millisecond,
		SuccessThreshold: 2,
		FailureWindow:    10 * time.Second,
		ProbeInterval:    50 * time.Millisecond,
	}
	breaker := circuit.NewBreaker(rdb, cfg)

	now := time.Now()
	breaker.SetNowFunc(func() time.Time {
		return now
	})

	t.Cleanup(func() {
		mr.Close()
		rdb.Close()
	})

	return breaker, &now
}

func TestGet_NoState(t *testing.T) {
	breaker, _ := setupBreaker(t)
	endpoint := domain.HashEndpoint("http://example.com")
	ctx := context.Background()

	cb, err := breaker.Get(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitClosed, cb.State)
}

func TestShouldAllow(t *testing.T) {
	breaker, mockNow := setupBreaker(t)
	endpoint := domain.HashEndpoint("http://example.com")
	ctx := context.Background()

	// Closed: should always allow
	allowed, isProbe, err := breaker.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.False(t, isProbe)

	// Transition to Open
	for i := 0; i < 3; i++ {
		_, err := breaker.RecordFailure(ctx, endpoint)
		require.NoError(t, err)
	}

	// Open (before recovery): should not allow
	allowed, isProbe, err = breaker.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.False(t, isProbe)

	// Open (after recovery): should allow as probe and transition to HalfOpen
	*mockNow = mockNow.Add(150 * time.Millisecond)
	allowed, isProbe, err = breaker.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, isProbe)

	cb, _ := breaker.Get(ctx, endpoint)
	assert.Equal(t, domain.CircuitHalfOpen, cb.State)

	// HalfOpen (before probe interval): should not allow
	allowed, isProbe, err = breaker.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.False(t, isProbe)

	// HalfOpen (after probe interval): should allow as probe
	*mockNow = mockNow.Add(60 * time.Millisecond)
	allowed, isProbe, err = breaker.ShouldAllow(ctx, endpoint)
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.True(t, isProbe)
}

func TestRecordSuccess(t *testing.T) {
	breaker, mockNow := setupBreaker(t)
	endpoint := domain.HashEndpoint("http://example.com")
	ctx := context.Background()

	// Record initial failures to move to Open
	for i := 0; i < 3; i++ {
		_, err := breaker.RecordFailure(ctx, endpoint)
		require.NoError(t, err)
	}

	// Fast forward time to allow recovery to HalfOpen
	*mockNow = mockNow.Add(150 * time.Millisecond)
	_, _, err := breaker.ShouldAllow(ctx, endpoint) // this will transition to half-open
	require.NoError(t, err)

	// HalfOpen (below threshold): should increment successes
	cb, err := breaker.RecordSuccess(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitHalfOpen, cb.State)
	assert.Equal(t, 1, cb.Successes)

	// HalfOpen (reaches threshold): should transition to Closed
	cb, err = breaker.RecordSuccess(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitClosed, cb.State)
	assert.Equal(t, 0, cb.Failures)
	assert.Equal(t, 0, cb.Successes)

	// Closed: should reset failures
	_, err = breaker.RecordFailure(ctx, endpoint) // add a failure
	require.NoError(t, err)
	cb, err = breaker.Get(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, 1, cb.Failures)

	cb, err = breaker.RecordSuccess(ctx, endpoint) // success should reset it
	require.NoError(t, err)
	assert.Equal(t, 0, cb.Failures)
}

func TestRecordFailure(t *testing.T) {
	breaker, mockNow := setupBreaker(t)
	endpoint := domain.HashEndpoint("http://example.com")
	ctx := context.Background()

	// Closed (below threshold): should increment failures
	cb, err := breaker.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitClosed, cb.State)
	assert.Equal(t, 1, cb.Failures)

	// Closed (reaches threshold): should transition to Open
	_, err = breaker.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	cb, err = breaker.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitOpen, cb.State)
	assert.Equal(t, 3, cb.Failures)

	// HalfOpen: should transition back to Open
	// recover to half-open
	*mockNow = mockNow.Add(150 * time.Millisecond)
	_, _, err = breaker.ShouldAllow(ctx, endpoint) // this will transition to half-open
	require.NoError(t, err)
	// record a failure
	cb, err = breaker.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, domain.CircuitOpen, cb.State)

	// WindowExpiry: failure count should reset
	*mockNow = mockNow.Add(11 * time.Second)
	cb, err = breaker.RecordFailure(ctx, endpoint)
	require.NoError(t, err)
	assert.Equal(t, 1, cb.Failures)
}