package hotstate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

const (
	statusPrefix       = "status:"
	retryQueue         = "retries"     // ZSET - stores references like "webhook_id:attempt"
	retryDataPrefix    = "retry_data:" // String (Compressed JSON) - stores per-reference data
	retryMetaPrefix    = "retry_meta:" // String (JSON) - stores lightweight metadata
	processedPrefix    = "processed:"
	statsPrefix        = "stats:"
	hllPrefix          = "hll:"
	circuitStatePrefix = "circuit:"
	scriptPrefix       = "script:"
)

// RedisHotState implements HotState using Redis
type RedisHotState struct {
	rdb           *redis.Client
	config        config.RedisConfig
	circuitConfig config.CircuitBreakerConfig
	ttlConfig     config.TTLConfig
}

// NewRedisHotState creates a new Redis-backed hot state
func NewRedisHotState(cfg config.RedisConfig, cbConfig config.CircuitBreakerConfig, ttlConfig config.TTLConfig) (*RedisHotState, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: cfg.MinIdleConns,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
	})

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return &RedisHotState{
		rdb:           rdb,
		config:        cfg,
		circuitConfig: cbConfig,
		ttlConfig:     ttlConfig,
	}, nil
}

func (r *RedisHotState) circuitKey(endpointHash domain.EndpointHash) string {
	return circuitStatePrefix + string(endpointHash)
}

// --- Circuit Breaker Operations ---

func (r *RedisHotState) GetCircuit(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := r.circuitKey(endpointHash)
	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		// No circuit state = closed by default
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

func (r *RedisHotState) UpdateCircuit(ctx context.Context, cb *domain.CircuitBreaker) error {
	data, err := json.Marshal(cb)
	if err != nil {
		return fmt.Errorf("marshal circuit: %w", err)
	}

	ttl := r.ttlConfig.CircuitStateTTL
	if cb.State == domain.CircuitClosed && cb.Failures == 0 {
		ttl = 2 * r.circuitConfig.RecoveryTimeout
	}

	return r.rdb.Set(ctx, r.circuitKey(cb.EndpointHash), data, ttl).Err()
}

func (r *RedisHotState) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := r.circuitKey(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := r.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = r.GetCircuit(ctx, endpointHash)
		if err != nil {
			return err
		}

		cb.LastSuccessAt = &now

		switch cb.State {
		case domain.CircuitClosed:
			cb.Failures = 0
		case domain.CircuitHalfOpen:
			cb.Successes++
			if cb.Successes >= r.circuitConfig.SuccessThreshold {
				cb.State = domain.CircuitClosed
				cb.StateChangedAt = now
				cb.Failures = 0
				cb.Successes = 0
				cb.NextProbeAt = nil
			}
		case domain.CircuitOpen:
			cb.State = domain.CircuitHalfOpen
			cb.StateChangedAt = now
			cb.Successes = 1
		}

		return r.UpdateCircuit(ctx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record success: %w", err)
	}

	return cb, nil
}

func (r *RedisHotState) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := r.circuitKey(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := r.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = r.GetCircuit(ctx, endpointHash)
		if err != nil {
			return err
		}

		lastFailureAt := cb.LastFailureAt
		cb.LastFailureAt = &now

		windowStart := now.Add(-r.circuitConfig.FailureWindow)
		if lastFailureAt != nil && lastFailureAt.After(windowStart) {
			cb.Failures++
		} else {
			cb.Failures = 1
		}

		switch cb.State {
		case domain.CircuitClosed:
			if cb.Failures >= r.circuitConfig.FailureThreshold {
				cb.State = domain.CircuitOpen
				cb.StateChangedAt = now
			}
		case domain.CircuitHalfOpen:
			cb.State = domain.CircuitOpen
			cb.StateChangedAt = now
			cb.Successes = 0
			cb.NextProbeAt = nil
		case domain.CircuitOpen:
			// Already open
		}

		return r.UpdateCircuit(ctx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record failure: %w", err)
	}

	return cb, nil
}

// --- Retry Scheduling (New Implementation with Visibility Timeout) ---

// StoreRetryData stores the compressed webhook data (one per webhook)
// The TTL is refreshed on each call, keeping data alive for active retries
func (r *RedisHotState) StoreRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error {
	compressed, err := compressWebhook(webhook)
	if err != nil {
		return err
	}

	// One key per webhook: "retry_data:webhook_id"
	key := retryDataPrefix + string(webhook.ID)
	return r.rdb.Set(ctx, key, compressed, ttl).Err()
}

// ScheduleRetry schedules the reference in ZSET
func (r *RedisHotState) ScheduleRetry(ctx context.Context, webhookID domain.WebhookID, attempt int, scheduledAt time.Time, reason string) error {
	reference := fmt.Sprintf("%s:%d", webhookID, attempt)
	score := float64(scheduledAt.Unix())

	// Store metadata (lightweight)
	metaKey := retryMetaPrefix + reference
	meta := map[string]interface{}{
		"reason":       reason,
		"scheduled_at": scheduledAt.Format(time.RFC3339),
	}
	metaJSON, _ := json.Marshal(meta)

	pipe := r.rdb.Pipeline()
	// Metadata has same TTL as data fallback
	pipe.Set(ctx, metaKey, metaJSON, r.ttlConfig.RetryDataTTL)
	pipe.ZAdd(ctx, retryQueue, redis.Z{
		Score:  score,
		Member: reference,
	})

	_, err := pipe.Exec(ctx)
	return err
}

// PopAndLockRetries atomically pops due retries and pushes their score to the future (visibility timeout)
// Returns list of references (webhook_id:attempt)
func (r *RedisHotState) PopAndLockRetries(ctx context.Context, limit int, lockDuration time.Duration) ([]string, error) {
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

	result, err := script.Run(ctx, r.rdb, []string{retryQueue}, now, limit, newScore).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("pop and lock retries: %w", err)
	}

	return result, nil
}

// GetRetryData retrieves and decompresses webhook data by webhookID
func (r *RedisHotState) GetRetryData(ctx context.Context, webhookID domain.WebhookID) (*domain.Webhook, error) {
	key := retryDataPrefix + string(webhookID)
	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("retry data not found for %s", webhookID)
	}
	if err != nil {
		return nil, fmt.Errorf("get retry data: %w", err)
	}

	// Decompress
	return decompressWebhook(data)
}

// AckRetry confirms processing and deletes reference from ZSET + metadata
// NOTE: Does NOT delete data - data is deleted only on terminal state by DeleteRetryData
func (r *RedisHotState) AckRetry(ctx context.Context, reference string) error {
	pipe := r.rdb.Pipeline()

	// 1. Remove from ZSET (the lock/schedule)
	pipe.ZRem(ctx, retryQueue, reference)

	// 2. Remove Metadata
	pipe.Del(ctx, retryMetaPrefix+reference)

	// NOTE: We do NOT delete data here - it's shared across attempts
	// and deleted only on terminal state (success or DLQ)

	_, err := pipe.Exec(ctx)
	return err
}

// DeleteRetryData deletes the compressed webhook data
// Call this on terminal states (success or DLQ/exhaustion)
func (r *RedisHotState) DeleteRetryData(ctx context.Context, webhookID domain.WebhookID) error {
	key := retryDataPrefix + string(webhookID)
	return r.rdb.Del(ctx, key).Err()
}

// --- Status Operations ---

func (r *RedisHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	key := statusPrefix + string(status.WebhookID)

	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}

	return r.rdb.Set(ctx, key, data, r.ttlConfig.StatusTTL).Err()
}

func (r *RedisHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	key := statusPrefix + string(webhookID)

	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}

	var status domain.WebhookStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("unmarshal status: %w", err)
	}

	return &status, nil
}

// --- Stats Operations ---

func (r *RedisHotState) IncrStats(ctx context.Context, bucket string, counters map[string]int64) error {
	pipe := r.rdb.Pipeline()

	for name, delta := range counters {
		key := statsPrefix + name + ":" + bucket
		pipe.IncrBy(ctx, key, delta)
		pipe.Expire(ctx, key, r.ttlConfig.StatsTTL)
	}

	_, err := pipe.Exec(ctx)
	return err
}

func (r *RedisHotState) AddToHLL(ctx context.Context, key string, values ...string) error {
	if len(values) == 0 {
		return nil
	}

	fullKey := hllPrefix + key
	args := make([]interface{}, len(values))
	for i, v := range values {
		args[i] = v
	}

	pipe := r.rdb.Pipeline()
	pipe.PFAdd(ctx, fullKey, args...)
	pipe.Expire(ctx, fullKey, r.ttlConfig.StatsTTL)
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RedisHotState) GetStats(ctx context.Context, from, to time.Time) (*domain.Stats, error) {
	stats := &domain.Stats{}

	// Generate hourly bucket keys for the time range
	current := from.Truncate(time.Hour)
	var buckets []string
	for !current.After(to) {
		buckets = append(buckets, current.Format("2006010215"))
		current = current.Add(time.Hour)
	}

	// Sum counters for each bucket
	pipe := r.rdb.Pipeline()
	cmds := make(map[string]*redis.StringCmd)

	for _, bucket := range buckets {
		for _, stat := range []string{"enqueued", "delivered", "failed", "exhausted"} {
			key := statsPrefix + stat + ":" + bucket
			cmds[stat+":"+bucket] = pipe.Get(ctx, key)
		}
	}

	// Get HLL counts
	hllKeys := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		hllKeys = append(hllKeys, hllPrefix+"endpoints:"+bucket)
	}

	pipe.Exec(ctx)

	// Aggregate results
	for _, bucket := range buckets {
		if val, err := cmds["enqueued:"+bucket].Int64(); err == nil {
			stats.Enqueued += val
		}
		if val, err := cmds["delivered:"+bucket].Int64(); err == nil {
			stats.Delivered += val
		}
		if val, err := cmds["failed:"+bucket].Int64(); err == nil {
			stats.Failed += val
		}
		if val, err := cmds["exhausted:"+bucket].Int64(); err == nil {
			stats.Exhausted += val
		}
	}

	// Get unique endpoint count (merge HLLs)
	if len(hllKeys) > 0 {
		stats.UniqueEndpoints, _ = r.rdb.PFCount(ctx, hllKeys...).Result()
	}

	return stats, nil
}

// --- Idempotency ---

func (r *RedisHotState) CheckAndSetProcessed(ctx context.Context, webhookID domain.WebhookID, attempt int, ttl time.Duration) (bool, error) {
	key := processedPrefix + string(webhookID) + ":" + strconv.Itoa(attempt)

	// SETNX returns true if the key was set (not processed before)
	set, err := r.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, fmt.Errorf("check processed: %w", err)
	}

	return set, nil
}

// --- Script Operations ---

// GetScript retrieves a script configuration from Redis cache
// Returns the JSON-encoded ScriptConfig or error if not found
func (r *RedisHotState) GetScript(ctx context.Context, configID domain.ConfigID) (string, error) {
	key := scriptPrefix + string(configID)
	data, err := r.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("script not found for config_id: %s", configID)
	}
	if err != nil {
		return "", fmt.Errorf("get script: %w", err)
	}
	return data, nil
}

// SetScript stores a script configuration in Redis cache
// scriptJSON should be the JSON-encoded ScriptConfig
func (r *RedisHotState) SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error {
	key := scriptPrefix + string(configID)
	if err := r.rdb.Set(ctx, key, scriptJSON, ttl).Err(); err != nil {
		return fmt.Errorf("set script: %w", err)
	}
	return nil
}

// DeleteScript removes a script configuration from Redis cache
func (r *RedisHotState) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	key := scriptPrefix + string(configID)
	if err := r.rdb.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("delete script: %w", err)
	}
	return nil
}

// --- Admin Operations ---

func (r *RedisHotState) Ping(ctx context.Context) error {
	return r.rdb.Ping(ctx).Err()
}

func (r *RedisHotState) FlushForRebuild(ctx context.Context) error {
	// Only flush our keys, not the entire database
	patterns := []string{
		statusPrefix + "*",
		retryQueue,
		retryDataPrefix + "*",
		retryMetaPrefix + "*",
		processedPrefix + "*",
		statsPrefix + "*",
		hllPrefix + "*",
		circuitStatePrefix + "*",
	}

	for _, pattern := range patterns {
		if pattern == retryQueue {
			r.rdb.Del(ctx, retryQueue)
			continue
		}

		iter := r.rdb.Scan(ctx, 0, pattern, 1000).Iterator()
		var keys []string
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
			if len(keys) >= 1000 {
				r.rdb.Del(ctx, keys...)
				keys = keys[:0]
			}
		}
		if len(keys) > 0 {
			r.rdb.Del(ctx, keys...)
		}
	}

	return nil
}

func (r *RedisHotState) Close() error {
	return r.rdb.Close()
}

// Client returns the underlying Redis client (for circuit breaker)
// NOTE: This should ideally not be exposed if HotState fully encapsulates Redis interactions.
// It's kept for backward compatibility/existing usage if any component directly uses it.
func (r *RedisHotState) Client() *redis.Client {
	return r.rdb
}
