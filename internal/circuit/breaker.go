package circuit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

const (
	keyPrefix = "circuit:"
)

// Breaker implements circuit breaker logic with Redis storage
type Breaker struct {
	rdb    *redis.Client
	config config.CircuitBreakerConfig
}

// NewBreaker creates a new circuit breaker
func NewBreaker(rdb *redis.Client, cfg config.CircuitBreakerConfig) *Breaker {
	return &Breaker{
		rdb:    rdb,
		config: cfg,
	}
}

func (b *Breaker) key(endpointHash domain.EndpointHash) string {
	return keyPrefix + string(endpointHash)
}

// Get retrieves the current circuit state
func (b *Breaker) Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	data, err := b.rdb.Get(ctx, b.key(endpointHash)).Bytes()
	if err == redis.Nil {
		// No circuit state = closed
		return &domain.CircuitBreaker{
			EndpointHash:   endpointHash,
			State:          domain.CircuitClosed,
			Failures:       0,
			StateChangedAt: time.Now(),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get circuit: %w", err)
	}

	var cb domain.CircuitBreaker
	if err := json.Unmarshal(data, &cb); err != nil {
		return nil, fmt.Errorf("unmarshal circuit: %w", err)
	}

	return &cb, nil
}

// ShouldAllow checks if a request should be allowed through the circuit
// Returns: (allowed, isProbe, error)
func (b *Breaker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	cb, err := b.Get(ctx, endpointHash)
	if err != nil {
		// On error, allow the request (fail open)
		return true, false, err
	}

	now := time.Now()

	switch cb.State {
	case domain.CircuitClosed:
		return true, false, nil

	case domain.CircuitOpen:
		// Check if it's time to transition to half-open
		if now.After(cb.StateChangedAt.Add(b.config.RecoveryTimeout)) {
			// Transition to half-open
			cb.State = domain.CircuitHalfOpen
			cb.StateChangedAt = now
			cb.Successes = 0
			nextProbe := now.Add(b.config.ProbeInterval)
			cb.NextProbeAt = &nextProbe
			if err := b.save(ctx, cb); err != nil {
				return false, false, err
			}
			// Allow this request as a probe
			return true, true, nil
		}
		// Circuit is open, don't allow
		return false, false, nil

	case domain.CircuitHalfOpen:
		// Only allow if it's time for a probe
		if cb.NextProbeAt == nil || now.After(*cb.NextProbeAt) {
			// Update next probe time
			nextProbe := now.Add(b.config.ProbeInterval)
			cb.NextProbeAt = &nextProbe
			if err := b.save(ctx, cb); err != nil {
				return true, true, err
			}
			return true, true, nil
		}
		// Not time for a probe yet
		return false, false, nil

	default:
		return true, false, nil
	}
}

// RecordSuccess records a successful delivery
func (b *Breaker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	// Use optimistic locking with WATCH/MULTI/EXEC for atomic updates
	key := b.key(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := b.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = b.getWithTx(ctx, tx, endpointHash)
		if err != nil {
			return err
		}

		cb.LastSuccessAt = &now

		switch cb.State {
		case domain.CircuitClosed:
			// Reset failure count on success
			cb.Failures = 0

		case domain.CircuitHalfOpen:
			cb.Successes++
			if cb.Successes >= b.config.SuccessThreshold {
				// Transition to closed
				cb.State = domain.CircuitClosed
				cb.StateChangedAt = now
				cb.Failures = 0
				cb.Successes = 0
				cb.NextProbeAt = nil
			}

		case domain.CircuitOpen:
			// Shouldn't happen, but handle gracefully
			cb.State = domain.CircuitHalfOpen
			cb.StateChangedAt = now
			cb.Successes = 1
		}

		return b.saveWithTx(ctx, tx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record success: %w", err)
	}

	return cb, nil
}

// RecordFailure records a failed delivery
func (b *Breaker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := b.key(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := b.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = b.getWithTx(ctx, tx, endpointHash)
		if err != nil {
			return err
		}

		cb.LastFailureAt = &now

		switch cb.State {
		case domain.CircuitClosed:
			// Check if failure is within the window
			windowStart := now.Add(-b.config.FailureWindow)
			if cb.LastFailureAt == nil || cb.LastFailureAt.Before(windowStart) {
				// First failure in window, reset count
				cb.Failures = 1
			} else {
				cb.Failures++
			}

			// Check if we should open the circuit
			if cb.Failures >= b.config.FailureThreshold {
				cb.State = domain.CircuitOpen
				cb.StateChangedAt = now
			}

		case domain.CircuitHalfOpen:
			// Any failure in half-open goes back to open
			cb.State = domain.CircuitOpen
			cb.StateChangedAt = now
			cb.Successes = 0
			cb.NextProbeAt = nil

		case domain.CircuitOpen:
			// Already open, just update failure time
			cb.Failures++
		}

		return b.saveWithTx(ctx, tx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record failure: %w", err)
	}

	return cb, nil
}

func (b *Breaker) getWithTx(ctx context.Context, tx *redis.Tx, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	data, err := tx.Get(ctx, b.key(endpointHash)).Bytes()
	if err == redis.Nil {
		return &domain.CircuitBreaker{
			EndpointHash:   endpointHash,
			State:          domain.CircuitClosed,
			Failures:       0,
			StateChangedAt: time.Now(),
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var cb domain.CircuitBreaker
	if err := json.Unmarshal(data, &cb); err != nil {
		return nil, err
	}
	return &cb, nil
}

func (b *Breaker) save(ctx context.Context, cb *domain.CircuitBreaker) error {
	data, err := json.Marshal(cb)
	if err != nil {
		return err
	}

	// Expire circuit state after 2x recovery timeout if closed
	// Keep longer if open/half-open
	ttl := 24 * time.Hour
	if cb.State == domain.CircuitClosed && cb.Failures == 0 {
		ttl = 2 * b.config.RecoveryTimeout
	}

	return b.rdb.Set(ctx, b.key(cb.EndpointHash), data, ttl).Err()
}

func (b *Breaker) saveWithTx(ctx context.Context, tx *redis.Tx, cb *domain.CircuitBreaker) error {
	data, err := json.Marshal(cb)
	if err != nil {
		return err
	}

	ttl := 24 * time.Hour
	if cb.State == domain.CircuitClosed && cb.Failures == 0 {
		ttl = 2 * b.config.RecoveryTimeout
	}

	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		return pipe.Set(ctx, b.key(cb.EndpointHash), data, ttl).Err()
	})
	return err
}

// GetDelayForState returns the retry delay based on circuit state
func (b *Breaker) GetDelayForState(state domain.CircuitState, baseDelay time.Duration) time.Duration {
	switch state {
	case domain.CircuitOpen:
		// When circuit is open, delay until recovery timeout
		return b.config.RecoveryTimeout
	case domain.CircuitHalfOpen:
		// In half-open, use probe interval
		return b.config.ProbeInterval
	default:
		// Normal exponential backoff
		return baseDelay
	}
}
