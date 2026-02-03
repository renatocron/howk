//go:build integration

package testutil

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/hotstate"
)

// PrefixedHotState wraps a RedisHotState and prefixes all keys.
// This enables multiple tests to use the same Redis DB without conflicts.
type PrefixedHotState struct {
	inner  *hotstate.RedisHotState
	prefix string
}

// NewPrefixedHotState wraps an existing RedisHotState with key prefixing.
func NewPrefixedHotState(inner *hotstate.RedisHotState, prefix string) *PrefixedHotState {
	return &PrefixedHotState{
		inner:  inner,
		prefix: prefix,
	}
}

// prefixKey adds the prefix to a key.
func (p *PrefixedHotState) prefixKey(key string) string {
	return p.prefix + key
}

// prefixKeys adds the prefix to multiple keys.
func (p *PrefixedHotState) prefixKeys(keys []string) []string {
	prefixed := make([]string, len(keys))
	for i, key := range keys {
		prefixed[i] = p.prefixKey(key)
	}
	return prefixed
}

// Ensure *PrefixedHotState implements HotState interface
var _ hotstate.HotState = (*PrefixedHotState)(nil)

// Status operations

func (p *PrefixedHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	// Serialize the status and use prefixed key directly via Redis client
	// Since RedisHotState doesn't expose a way to override key prefixing,
	// we need to use the underlying client
	client := p.inner.Client()
	key := p.prefixKey("status:" + string(status.WebhookID))

	// Use the same serialization as RedisHotState
	data, err := defaultMarshal(status)
	if err != nil {
		return err
	}

	return client.Set(ctx, key, data, 7*24*time.Hour).Err()
}

func (p *PrefixedHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	client := p.inner.Client()
	key := p.prefixKey("status:" + string(webhookID))

	data, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var status domain.WebhookStatus
	if err := defaultUnmarshal(data, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// Retry scheduling

func (p *PrefixedHotState) EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error {
	// This is tricky - we need to use the same compression logic
	// For now, delegate to inner but with prefixed key via Lua or direct access
	// Since the inner hotstate uses hardcoded prefixes, we need to work around this

	client := p.inner.Client()
	key := p.prefixKey("retry_data:" + string(webhook.ID))

	// Check if key exists first
	exists, err := client.Expire(ctx, key, ttl).Result()
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// Need to compress the webhook
	compressed, err := compressWebhookData(webhook)
	if err != nil {
		return err
	}

	return client.Set(ctx, key, compressed, ttl).Err()
}

func (p *PrefixedHotState) ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error {
	client := p.inner.Client()
	reference := fmt.Sprintf("%s:%d", webhookID, attempt)

	// Store metadata
	metaKey := p.prefixKey("retry_meta:" + reference)

	// Use the retry queue with prefix
	retryQueueKey := p.prefixKey("retries")

	// Create metadata
	meta := map[string]interface{}{
		"reason":       reason,
		"scheduled_at": scheduledAt.Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	// We need to use a Lua script to atomically add to the sorted set
	// For simplicity, use pipeline
	pipe := client.Pipeline()
	pipe.Set(ctx, metaKey, metaJSON, 24*time.Hour)
	pipe.ZAdd(ctx, retryQueueKey, redis.Z{
		Score:  float64(scheduledAt.Unix()),
		Member: reference,
	})
	_, err := pipe.Exec(ctx)
	return err
}

func (p *PrefixedHotState) PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error) {
	client := p.inner.Client()
	retryQueueKey := p.prefixKey("retries")

	now := float64(time.Now().Unix())
	newScore := float64(time.Now().Add(lockDuration).Unix())

	// Lua script: Find due items, update their score to future (lock), return them
	script := redis.NewScript(`
		local items = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
		if #items > 0 then
			for i, member in ipairs(items) do
				redis.call('ZADD', KEYS[1], ARGV[3], member)
			end
		end
		return items
	`)

	return script.Run(ctx, client, []string{retryQueueKey}, now, limit, newScore).StringSlice()
}

func (p *PrefixedHotState) GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error) {
	client := p.inner.Client()
	key := p.prefixKey("retry_data:" + string(webhookID))

	data, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	return decompressWebhookData(data)
}

func (p *PrefixedHotState) AckRetry(ctx context.Context, reference string) error {
	client := p.inner.Client()
	retryQueueKey := p.prefixKey("retries")
	metaKey := p.prefixKey("retry_meta:" + reference)

	pipe := client.Pipeline()
	pipe.ZRem(ctx, retryQueueKey, reference)
	pipe.Del(ctx, metaKey)
	_, err := pipe.Exec(ctx)
	return err
}

func (p *PrefixedHotState) DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error {
	client := p.inner.Client()
	key := p.prefixKey("retry_data:" + string(webhookID))
	return client.Del(ctx, key).Err()
}

// Circuit breaker operations

func (p *PrefixedHotState) CircuitBreaker() hotstate.CircuitBreakerChecker {
	return &prefixedCircuitBreaker{
		inner:  p.inner.CircuitBreaker(),
		prefix: p.prefix,
		client: p.inner.Client(),
	}
}

// Stats

func (p *PrefixedHotState) IncrStats(ctx context.Context, bucket string, counters map[string]int64) error {
	client := p.inner.Client()
	pipe := client.Pipeline()

	for name, delta := range counters {
		key := p.prefixKey("stats:" + name + ":" + bucket)
		pipe.IncrBy(ctx, key, delta)
		pipe.Expire(ctx, key, 48*time.Hour)
	}

	_, err := pipe.Exec(ctx)
	return err
}

func (p *PrefixedHotState) AddToHLL(ctx context.Context, key string, values ...string) error {
	if len(values) == 0 {
		return nil
	}

	client := p.inner.Client()
	fullKey := p.prefixKey("hll:" + key)

	args := make([]interface{}, len(values))
	for i, v := range values {
		args[i] = v
	}

	pipe := client.Pipeline()
	pipe.PFAdd(ctx, fullKey, args...)
	pipe.Expire(ctx, fullKey, 48*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func (p *PrefixedHotState) GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error) {
	// This is complex - we'd need to scan for prefixed keys
	// For now, return empty stats
	return &domain.Stats{}, nil
}

// Idempotency

func (p *PrefixedHotState) CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error) {
	client := p.inner.Client()
	key := p.prefixKey(fmt.Sprintf("processed:%s:%d", webhookID, attempt))
	return client.SetNX(ctx, key, "1", ttl).Result()
}

// Script operations

func (p *PrefixedHotState) GetScript(ctx context.Context, configID domain.ConfigID) (string, error) {
	client := p.inner.Client()
	key := p.prefixKey("script:" + string(configID))
	return client.Get(ctx, key).Result()
}

func (p *PrefixedHotState) SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error {
	client := p.inner.Client()
	key := p.prefixKey("script:" + string(configID))
	return client.Set(ctx, key, scriptJSON, ttl).Err()
}

func (p *PrefixedHotState) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	client := p.inner.Client()
	key := p.prefixKey("script:" + string(configID))
	return client.Del(ctx, key).Err()
}

// Health & Admin

func (p *PrefixedHotState) Ping(ctx context.Context) error {
	return p.inner.Ping(ctx)
}

func (p *PrefixedHotState) FlushForRebuild(ctx context.Context) error {
	client := p.inner.Client()

	// Scan for all keys with our prefix and delete them
	pattern := p.prefix + "*"
	iter := client.Scan(ctx, 0, pattern, 1000).Iterator()

	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= 1000 {
			client.Del(ctx, keys...)
			keys = keys[:0]
		}
	}
	if len(keys) > 0 {
		client.Del(ctx, keys...)
	}

	return iter.Err()
}

func (p *PrefixedHotState) Close() error {
	return p.inner.Close()
}

func (p *PrefixedHotState) Client() *redis.Client {
	return p.inner.Client()
}

// IncrInflight atomically increments the in-flight counter for an endpoint.
func (p *PrefixedHotState) IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error) {
	client := p.inner.Client()
	key := p.prefixKey("concurrency:" + string(endpointHash))

	pipe := client.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, err
	}
	return incrCmd.Val(), nil
}

// DecrInflight atomically decrements the in-flight counter for an endpoint.
func (p *PrefixedHotState) DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error {
	client := p.inner.Client()
	key := p.prefixKey("concurrency:" + string(endpointHash))

	script := redis.NewScript(`
		local current = tonumber(redis.call('GET', KEYS[1]) or '0')
		if current > 0 then
			return redis.call('DECR', KEYS[1])
		end
		return 0
	`)

	_, err := script.Run(ctx, client, []string{key}).Int64()
	return err
}

// SystemEpoch operations

func (p *PrefixedHotState) GetEpoch(ctx context.Context) (*domain.SystemEpoch, error) {
	client := p.inner.Client()
	key := p.prefixKey("system:epoch")

	data, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var epoch domain.SystemEpoch
	if err := defaultUnmarshal(data, &epoch); err != nil {
		return nil, err
	}
	return &epoch, nil
}

func (p *PrefixedHotState) SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error {
	client := p.inner.Client()
	key := p.prefixKey("system:epoch")

	data, err := defaultMarshal(epoch)
	if err != nil {
		return err
	}
	return client.Set(ctx, key, data, 0).Err()
}

func (p *PrefixedHotState) GetRetryQueueSize(ctx context.Context) (int64, error) {
	client := p.inner.Client()
	retryQueueKey := p.prefixKey("retries")
	return client.ZCard(ctx, retryQueueKey).Result()
}

// prefixedCircuitBreaker wraps the circuit breaker with prefixed keys
type prefixedCircuitBreaker struct {
	inner  hotstate.CircuitBreakerChecker
	prefix string
	client *redis.Client
}

func (pcb *prefixedCircuitBreaker) Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	// Try to get from Redis directly with prefix
	key := pcb.prefix + "circuit:" + string(endpointHash)
	data, err := pcb.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return &domain.CircuitBreaker{
				EndpointHash:   endpointHash,
				State:          domain.CircuitClosed,
				Failures:       0,
				StateChangedAt: time.Now(),
			}, nil
		}
		return nil, err
	}

	var cb domain.CircuitBreaker
	if err := defaultUnmarshal(data, &cb); err != nil {
		return nil, err
	}
	return &cb, nil
}

func (pcb *prefixedCircuitBreaker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	// For simplicity, delegate to inner but this may not have the right state
	// A full implementation would replicate the logic with prefixed keys
	cb, err := pcb.Get(ctx, endpointHash)
	if err != nil {
		return true, false, err
	}

	// Simple implementation - check state
	switch cb.State {
	case domain.CircuitClosed:
		return true, false, nil
	case domain.CircuitOpen:
		return false, false, nil
	case domain.CircuitHalfOpen:
		return true, true, nil
	default:
		return true, false, nil
	}
}

func (pcb *prefixedCircuitBreaker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := pcb.prefix + "circuit:" + string(endpointHash)
	cb, err := pcb.Get(ctx, endpointHash)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	cb.LastSuccessAt = &now

	switch cb.State {
	case domain.CircuitClosed:
		cb.Failures = 0
	case domain.CircuitHalfOpen:
		cb.Successes++
		if cb.Successes >= 2 { // Use default success threshold
			cb.State = domain.CircuitClosed
			cb.StateChangedAt = now
			cb.Failures = 0
			cb.Successes = 0
		}
	case domain.CircuitOpen:
		cb.State = domain.CircuitHalfOpen
		cb.StateChangedAt = now
		cb.Successes = 1
	}

	data, err := defaultMarshal(cb)
	if err != nil {
		return nil, err
	}

	return cb, pcb.client.Set(ctx, key, data, 24*time.Hour).Err()
}

func (pcb *prefixedCircuitBreaker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := pcb.prefix + "circuit:" + string(endpointHash)
	cb, err := pcb.Get(ctx, endpointHash)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	cb.LastFailureAt = &now
	cb.Failures++

	switch cb.State {
	case domain.CircuitClosed:
		if cb.Failures >= 5 { // Use default failure threshold
			cb.State = domain.CircuitOpen
			cb.StateChangedAt = now
		}
	case domain.CircuitHalfOpen:
		cb.State = domain.CircuitOpen
		cb.StateChangedAt = now
		cb.Successes = 0
	}

	data, err := defaultMarshal(cb)
	if err != nil {
		return nil, err
	}

	return cb, pcb.client.Set(ctx, key, data, 24*time.Hour).Err()
}

// Helper functions for serialization

func defaultMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func defaultUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// Compression helpers - need to match hotstate implementation
func compressWebhookData(webhook *domain.Webhook) ([]byte, error) {
	data, err := json.Marshal(webhook)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decompressWebhookData(data []byte) (*domain.Webhook, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()

	uncompressed, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var webhook domain.Webhook
	if err := json.Unmarshal(uncompressed, &webhook); err != nil {
		return nil, err
	}
	return &webhook, nil
}
