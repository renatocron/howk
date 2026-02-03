//go:build integration

package testutil

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// PrefixedRedis wraps a redis.Client and automatically applies a key prefix.
// This allows tests to use direct Redis operations with automatic key isolation.
type PrefixedRedis struct {
	client *redis.Client
	prefix string
}

// NewPrefixedRedis creates a new PrefixedRedis wrapper.
func NewPrefixedRedis(client *redis.Client, prefix string) *PrefixedRedis {
	return &PrefixedRedis{
		client: client,
		prefix: prefix,
	}
}

// prefixKey adds the prefix to a key.
func (p *PrefixedRedis) prefixKey(key string) string {
	return p.prefix + key
}

// Get returns the value for the given key with prefix applied.
func (p *PrefixedRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	return p.client.Get(ctx, p.prefixKey(key))
}

// Set sets the value for the given key with prefix applied.
func (p *PrefixedRedis) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.StatusCmd {
	return p.client.Set(ctx, p.prefixKey(key), value, expiration)
}

// SetNX sets the value if the key doesn't exist, with prefix applied.
func (p *PrefixedRedis) SetNX(ctx context.Context, key string, value interface{}, expiration time.Duration) *redis.BoolCmd {
	return p.client.SetNX(ctx, p.prefixKey(key), value, expiration)
}

// Del deletes the given keys with prefix applied.
func (p *PrefixedRedis) Del(ctx context.Context, keys ...string) *redis.IntCmd {
	prefixedKeys := make([]string, len(keys))
	for i, key := range keys {
		prefixedKeys[i] = p.prefixKey(key)
	}
	return p.client.Del(ctx, prefixedKeys...)
}

// ZAdd adds members to a sorted set with prefix applied.
func (p *PrefixedRedis) ZAdd(ctx context.Context, key string, members ...redis.Z) *redis.IntCmd {
	return p.client.ZAdd(ctx, p.prefixKey(key), members...)
}

// ZRem removes members from a sorted set with prefix applied.
func (p *PrefixedRedis) ZRem(ctx context.Context, key string, members ...interface{}) *redis.IntCmd {
	return p.client.ZRem(ctx, p.prefixKey(key), members...)
}

// ZCount returns the count of members in a sorted set within a score range, with prefix applied.
func (p *PrefixedRedis) ZCount(ctx context.Context, key, min, max string) *redis.IntCmd {
	return p.client.ZCount(ctx, p.prefixKey(key), min, max)
}

// ZCard returns the cardinality of a sorted set, with prefix applied.
func (p *PrefixedRedis) ZCard(ctx context.Context, key string) *redis.IntCmd {
	return p.client.ZCard(ctx, p.prefixKey(key))
}

// Incr increments the value for the given key, with prefix applied.
func (p *PrefixedRedis) Incr(ctx context.Context, key string) *redis.IntCmd {
	return p.client.Incr(ctx, p.prefixKey(key))
}

// IncrBy increments the value by the given amount, with prefix applied.
func (p *PrefixedRedis) IncrBy(ctx context.Context, key string, value int64) *redis.IntCmd {
	return p.client.IncrBy(ctx, p.prefixKey(key), value)
}

// Decr decrements the value for the given key, with prefix applied.
func (p *PrefixedRedis) Decr(ctx context.Context, key string) *redis.IntCmd {
	return p.client.Decr(ctx, p.prefixKey(key))
}

// Expire sets an expiration on a key, with prefix applied.
func (p *PrefixedRedis) Expire(ctx context.Context, key string, expiration time.Duration) *redis.BoolCmd {
	return p.client.Expire(ctx, p.prefixKey(key), expiration)
}

// TTL returns the remaining TTL for a key, with prefix applied.
func (p *PrefixedRedis) TTL(ctx context.Context, key string) *redis.DurationCmd {
	return p.client.TTL(ctx, p.prefixKey(key))
}

// Scan scans for keys matching the pattern with prefix applied.
// Note: The returned keys will have the prefix - you may need to strip it.
func (p *PrefixedRedis) Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd {
	return p.client.Scan(ctx, cursor, p.prefixKey(match), count)
}

// FlushPrefix deletes all keys with this prefix. Useful for cleanup.
func (p *PrefixedRedis) FlushPrefix(ctx context.Context) error {
	pattern := p.prefix + "*"
	iter := p.client.Scan(ctx, 0, pattern, 1000).Iterator()

	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= 1000 {
			p.client.Del(ctx, keys...)
			keys = keys[:0]
		}
	}
	if len(keys) > 0 {
		p.client.Del(ctx, keys...)
	}
	return iter.Err()
}

// Underlying returns the underlying *redis.Client for operations not covered by the wrapper.
func (p *PrefixedRedis) Underlying() *redis.Client {
	return p.client
}
