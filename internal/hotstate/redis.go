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
	statusPrefix    = "status:"
	retryQueue      = "retries"
	processedPrefix = "processed:"
	statsPrefix     = "stats:"
	hllPrefix       = "hll:"
)

// RedisHotState implements HotState using Redis
type RedisHotState struct {
	rdb    *redis.Client
	config config.RedisConfig
}

// NewRedisHotState creates a new Redis-backed hot state
func NewRedisHotState(cfg config.RedisConfig) (*RedisHotState, error) {
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
		rdb:    rdb,
		config: cfg,
	}, nil
}

// --- Status Operations ---

func (r *RedisHotState) SetStatus(ctx context.Context, status *domain.WebhookStatus) error {
	key := statusPrefix + string(status.WebhookID)

	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}

	// Status expires after 7 days
	return r.rdb.Set(ctx, key, data, 7*24*time.Hour).Err()
}

func (r *RedisHotState) GetStatus(ctx context.Context, webhookID domain.WebhookID) (*domain.WebhookStatus, error) {
	key := statusPrefix + string(webhookID)

	data, err := r.rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // Not found
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

// --- Retry Scheduling ---

func (r *RedisHotState) ScheduleRetry(ctx context.Context, msg *RetryMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal retry message: %w", err)
	}

	// ZADD with score = scheduled timestamp (unix)
	score := float64(msg.ScheduledAt.Unix())
	return r.rdb.ZAdd(ctx, retryQueue, redis.Z{
		Score:  score,
		Member: data,
	}).Err()
}

func (r *RedisHotState) PopDueRetries(ctx context.Context, limit int) ([]*RetryMessage, error) {
	now := float64(time.Now().Unix())

	// ZPOPMIN atomically pops the lowest scored items
	// We use a Lua script to pop only items with score <= now
	script := redis.NewScript(`
		local results = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
		if #results > 0 then
			redis.call('ZREM', KEYS[1], unpack(results))
		end
		return results
	`)

	results, err := script.Run(ctx, r.rdb, []string{retryQueue}, now, limit).StringSlice()
	if err != nil {
		return nil, fmt.Errorf("pop due retries: %w", err)
	}

	messages := make([]*RetryMessage, 0, len(results))
	for _, data := range results {
		var msg RetryMessage
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			// Log and skip malformed messages
			continue
		}
		messages = append(messages, &msg)
	}

	return messages, nil
}

// --- Stats Operations ---

func (r *RedisHotState) IncrStats(ctx context.Context, bucket string, counters map[string]int64) error {
	pipe := r.rdb.Pipeline()

	for name, delta := range counters {
		key := statsPrefix + name + ":" + bucket
		pipe.IncrBy(ctx, key, delta)
		// Expire after 48 hours
		pipe.Expire(ctx, key, 48*time.Hour)
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
	pipe.Expire(ctx, fullKey, 48*time.Hour)
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

// --- Admin Operations ---

func (r *RedisHotState) Ping(ctx context.Context) error {
	return r.rdb.Ping(ctx).Err()
}

func (r *RedisHotState) FlushForRebuild(ctx context.Context) error {
	// Only flush our keys, not the entire database
	// This is a pattern scan + delete
	patterns := []string{
		statusPrefix + "*",
		retryQueue,
		processedPrefix + "*",
		statsPrefix + "*",
		hllPrefix + "*",
		"circuit:*",
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
func (r *RedisHotState) Client() *redis.Client {
	return r.rdb
}
