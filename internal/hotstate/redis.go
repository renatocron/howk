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
	concurrencyPrefix  = "concurrency:"
	systemEpochKey     = "howk:system:epoch"
	canaryKey          = "howk:system:initialized"
	reconcilerLockKey  = "howk:reconciler:lock"
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

// circuitBreaker implements CircuitBreakerChecker interface
type circuitBreaker struct {
	rdb       *redis.Client
	config    config.CircuitBreakerConfig
	ttlConfig config.TTLConfig
}

func (r *RedisHotState) CircuitBreaker() CircuitBreakerChecker {
	return &circuitBreaker{
		rdb:       r.rdb,
		config:    r.circuitConfig,
		ttlConfig: r.ttlConfig,
	}
}

func (c *circuitBreaker) circuitKey(endpointHash domain.EndpointHash) string {
	return circuitStatePrefix + string(endpointHash)
}

// Get retrieves the current circuit state
func (c *circuitBreaker) Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	data, err := c.rdb.Get(ctx, c.circuitKey(endpointHash)).Bytes()
	if err == redis.Nil {
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

// getWithTx retrieves circuit state within a transaction
func (c *circuitBreaker) getWithTx(ctx context.Context, tx *redis.Tx, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	data, err := tx.Get(ctx, c.circuitKey(endpointHash)).Bytes()
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

// save saves circuit state
func (c *circuitBreaker) save(ctx context.Context, cb *domain.CircuitBreaker) error {
	data, err := json.Marshal(cb)
	if err != nil {
		return err
	}

	ttl := c.ttlConfig.CircuitStateTTL
	if cb.State == domain.CircuitClosed && cb.Failures == 0 {
		ttl = 2 * c.config.RecoveryTimeout
	}

	return c.rdb.Set(ctx, c.circuitKey(cb.EndpointHash), data, ttl).Err()
}

// saveWithTx saves circuit state within a transaction
func (c *circuitBreaker) saveWithTx(ctx context.Context, tx *redis.Tx, cb *domain.CircuitBreaker) error {
	data, err := json.Marshal(cb)
	if err != nil {
		return err
	}

	ttl := c.ttlConfig.CircuitStateTTL
	if cb.State == domain.CircuitClosed && cb.Failures == 0 {
		ttl = 2 * c.config.RecoveryTimeout
	}

	_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		return pipe.Set(ctx, c.circuitKey(cb.EndpointHash), data, ttl).Err()
	})
	return err
}

// ShouldAllow checks if a request should be allowed through the circuit
func (c *circuitBreaker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	cb, err := c.Get(ctx, endpointHash)
	if err != nil {
		return true, false, err
	}

	now := time.Now()
	key := c.circuitKey(endpointHash)

	switch cb.State {
	case domain.CircuitClosed:
		return true, false, nil

	case domain.CircuitOpen:
		if now.After(cb.StateChangedAt.Add(c.config.RecoveryTimeout)) {
			var transitioned bool
			err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
				currentCB, err := c.getWithTx(ctx, tx, endpointHash)
				if err != nil {
					return err
				}

				if currentCB.State != domain.CircuitOpen {
					return nil
				}

				currentCB.State = domain.CircuitHalfOpen
				currentCB.StateChangedAt = now
				currentCB.Successes = 0
				nextProbe := now.Add(c.config.ProbeInterval)
				currentCB.NextProbeAt = &nextProbe

				transitioned = true
				return c.saveWithTx(ctx, tx, currentCB)
			}, key)

			if err != nil {
				return false, false, err
			}

			if transitioned {
				return true, true, nil
			}
		}
		return false, false, nil

	case domain.CircuitHalfOpen:
		lockKey := fmt.Sprintf("probe_lock:%s", endpointHash)

		if cb.NextProbeAt == nil || now.After(*cb.NextProbeAt) {
			locked, err := c.rdb.SetNX(ctx, lockKey, "1", c.config.ProbeInterval).Result()
			if err != nil {
				return true, true, err
			}

			if !locked {
				return false, false, nil
			}

			nextProbe := now.Add(c.config.ProbeInterval)
			cb.NextProbeAt = &nextProbe
			if err := c.save(ctx, cb); err != nil {
				return true, true, err
			}

			return true, true, nil
		}
		return false, false, nil

	default:
		return true, false, nil
	}
}

// RecordSuccess records a successful delivery
func (c *circuitBreaker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := c.circuitKey(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = c.getWithTx(ctx, tx, endpointHash)
		if err != nil {
			return err
		}

		cb.LastSuccessAt = &now

		switch cb.State {
		case domain.CircuitClosed:
			cb.Failures = 0
		case domain.CircuitHalfOpen:
			cb.Successes++
			if cb.Successes >= c.config.SuccessThreshold {
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

		return c.saveWithTx(ctx, tx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record success: %w", err)
	}

	return cb, nil
}

// RecordFailure records a failed delivery
func (c *circuitBreaker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	key := c.circuitKey(endpointHash)
	now := time.Now()

	var cb *domain.CircuitBreaker

	err := c.rdb.Watch(ctx, func(tx *redis.Tx) error {
		var err error
		cb, err = c.getWithTx(ctx, tx, endpointHash)
		if err != nil {
			return err
		}

		lastFailureAt := cb.LastFailureAt
		cb.LastFailureAt = &now

		windowStart := now.Add(-c.config.FailureWindow)
		if lastFailureAt != nil && lastFailureAt.After(windowStart) {
			cb.Failures++
		} else {
			cb.Failures = 1
		}

		switch cb.State {
		case domain.CircuitClosed:
			if cb.Failures >= c.config.FailureThreshold {
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

		return c.saveWithTx(ctx, tx, cb)
	}, key)

	if err != nil {
		return nil, fmt.Errorf("record failure: %w", err)
	}

	return cb, nil
}

// --- Retry Scheduling (New Implementation with Visibility Timeout) ---

// EnsureRetryData ensures the webhook data exists in Redis.
// If it exists, it only refreshes the TTL (efficient - no compression/payload transfer).
// If it does not exist, it compresses and stores it.
func (r *RedisHotState) EnsureRetryData(ctx context.Context, webhook *domain.Webhook, ttl time.Duration) error {
	key := retryDataPrefix + string(webhook.ID)

	// 1. Try to refresh TTL first (Optimistic: most retries are subsequent attempts)
	// EXPIRE returns 1 (true) if key exists, 0 (false) if not
	exists, err := r.rdb.Expire(ctx, key, ttl).Result()
	if err != nil {
		return fmt.Errorf("refresh retry data ttl: %w", err)
	}

	if exists {
		// Data exists and TTL updated. We are done - no compression/payload transfer needed.
		return nil
	}

	// 2. Data missing (first retry or expired), perform full save with compression
	compressed, err := compressWebhook(webhook)
	if err != nil {
		return err
	}

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

// setStatusLWW is a Lua script implementing Last-Write-Wins semantics.
// It only updates if the new timestamp is greater than the existing one.
// KEYS[1] = status key
// ARGV[1] = new timestamp (nanoseconds)
// ARGV[2] = JSON data
// ARGV[3] = TTL in seconds
var setStatusLWW = redis.NewScript(`
	local key = KEYS[1]
	local new_ts = tonumber(ARGV[1])
	local data = ARGV[2]
	local ttl = tonumber(ARGV[3])
	
	local old_ts = tonumber(redis.call('HGET', key, 'ts') or '0')
	if new_ts > old_ts then
		redis.call('HSET', key, 'data', data, 'ts', new_ts)
		redis.call('EXPIRE', key, ttl)
		return 1
	end
	return 0
`)

// --- Status Operations ---

func (r *RedisHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	key := statusPrefix + string(status.WebhookID)

	// Ensure UpdatedAtNs is set (default to now if not provided)
	if status.UpdatedAtNs == 0 {
		status.UpdatedAtNs = time.Now().UnixNano()
	}

	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}

	_, err = setStatusLWW.Run(ctx, r.rdb, []string{key},
		status.UpdatedAtNs,
		string(data),
		int64(r.ttlConfig.StatusTTL.Seconds()),
	).Result()
	
	if err != nil {
		return fmt.Errorf("set status lww: %w", err)
	}
	return nil
}

func (r *RedisHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	key := statusPrefix + string(webhookID)

	// Get the data field from hash (LWW storage format)
	data, err := r.rdb.HGet(ctx, key, "data").Bytes()
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
	// Remove canary first (signals other instances that rebuild is in progress)
	r.DelCanary(ctx)

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
		concurrencyPrefix + "*",
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

// --- Concurrency Tracking (Penalty Box) ---

// IncrInflight atomically increments the in-flight counter for an endpoint.
// Returns the new count after increment.
// The TTL is refreshed on every call to keep the key alive during active delivery.
func (r *RedisHotState) IncrInflight(ctx context.Context, endpointHash domain.EndpointHash, ttl time.Duration) (int64, error) {
	key := concurrencyPrefix + string(endpointHash)
	pipe := r.rdb.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("incr inflight: %w", err)
	}
	return incrCmd.Val(), nil
}

// decrInflightScript is a Lua script that decrements the counter but never goes below zero.
// This prevents counter drift from edge cases like duplicate processing.
var decrInflightScript = redis.NewScript(`
	local key = KEYS[1]
	local current = tonumber(redis.call('GET', key) or '0')
	if current > 0 then
		return redis.call('DECR', key)
	end
	return 0
`)

// DecrInflight atomically decrements the in-flight counter for an endpoint.
// Uses a Lua script to ensure the counter never goes below zero.
func (r *RedisHotState) DecrInflight(ctx context.Context, endpointHash domain.EndpointHash) error {
	key := concurrencyPrefix + string(endpointHash)
	_, err := decrInflightScript.Run(ctx, r.rdb, []string{key}).Int64()
	if err != nil {
		return fmt.Errorf("decr inflight: %w", err)
	}
	return nil
}

// --- System Epoch Operations ---

// GetEpoch retrieves the system epoch marker from Redis
func (r *RedisHotState) GetEpoch(ctx context.Context) (*domain.SystemEpoch, error) {
	data, err := r.rdb.Get(ctx, systemEpochKey).Bytes()
	if err == redis.Nil {
		return nil, nil // No epoch set yet
	}
	if err != nil {
		return nil, fmt.Errorf("get epoch: %w", err)
	}

	var epoch domain.SystemEpoch
	if err := json.Unmarshal(data, &epoch); err != nil {
		return nil, fmt.Errorf("unmarshal epoch: %w", err)
	}
	return &epoch, nil
}

// SetEpoch sets the system epoch marker in Redis
func (r *RedisHotState) SetEpoch(ctx context.Context, epoch *domain.SystemEpoch) error {
	data, err := json.Marshal(epoch)
	if err != nil {
		return fmt.Errorf("marshal epoch: %w", err)
	}
	return r.rdb.Set(ctx, systemEpochKey, data, 0).Err()
}

// GetRetryQueueSize returns the current size of the retry queue
func (r *RedisHotState) GetRetryQueueSize(ctx context.Context) (int64, error) {
	return r.rdb.ZCard(ctx, retryQueue).Result()
}
// --- Zero Maintenance: Auto-Recovery (Sentinel Pattern) ---

// CheckCanary checks if the system canary key exists (indicates Redis is initialized)
func (r *RedisHotState) CheckCanary(ctx context.Context) (bool, error) {
	exists, err := r.rdb.Exists(ctx, canaryKey).Result()
	if err != nil {
		return false, fmt.Errorf("check canary: %w", err)
	}
	return exists > 0, nil
}

// SetCanary sets the system canary key (mark Redis as initialized)
func (r *RedisHotState) SetCanary(ctx context.Context) error {
	// Canary never expires - if Redis loses this key, we know we need reconciliation
	return r.rdb.Set(ctx, canaryKey, "1", 0).Err()
}

// DelCanary removes the canary key (used during FlushForRebuild)
func (r *RedisHotState) DelCanary(ctx context.Context) error {
	return r.rdb.Del(ctx, canaryKey).Err()
}

// WaitForCanary polls until the canary key appears or timeout
func (r *RedisHotState) WaitForCanary(ctx context.Context, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		exists, err := r.CheckCanary(ctx)
		if err == nil && exists {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			// Continue polling
		}
	}
}

// lockHeartbeat manages lock extension during long-running reconciliation
func (r *RedisHotState) lockHeartbeat(ctx context.Context, lockValue string, ttl time.Duration, stop chan struct{}) {
	ticker := time.NewTicker(ttl / 3) // Extend at 1/3 of TTL
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Extend lock TTL (only if we still own it)
			r.rdb.Eval(ctx, `
				if redis.call('GET', KEYS[1]) == ARGV[1] then
					return redis.call('PEXPIRE', KEYS[1], ARGV[2])
				end
				return 0
			`, []string{reconcilerLockKey}, lockValue, int64(ttl.Milliseconds()))
		}
	}
}

// AcquireReconcilerLock attempts to acquire a distributed lock for reconciliation.
// Returns true if lock acquired, and an unlock function to release it.
// The lock has a TTL and is automatically extended via heartbeat until unlock.
func (r *RedisHotState) AcquireReconcilerLock(ctx context.Context, ttl time.Duration) (bool, func()) {
	// Generate a unique lock value (instance identifier)
	lockValue := fmt.Sprintf("%d-%d", time.Now().UnixNano(), time.Now().Unix())

	// Try to acquire lock with SET NX EX
	acquired, err := r.rdb.SetNX(ctx, reconcilerLockKey, lockValue, ttl).Result()
	if err != nil || !acquired {
		return false, nil
	}

	// Start heartbeat to extend lock during long reconciliation
	stopHeartbeat := make(chan struct{})
	go r.lockHeartbeat(context.Background(), lockValue, ttl, stopHeartbeat)

	// Return unlock function
	unlock := func() {
		close(stopHeartbeat)
		// Only delete if we still own the lock
		r.rdb.Eval(ctx, `
			if redis.call('GET', KEYS[1]) == ARGV[1] then
				return redis.call('DEL', KEYS[1])
			end
			return 0
		`, []string{reconcilerLockKey}, lockValue)
	}

	return true, unlock
}
