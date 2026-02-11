package delivery

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
)

const domainConcurrencyPrefix = "domain_concurrency:"

// DomainLimiter controls per-domain outbound concurrency.
type DomainLimiter interface {
	// TryAcquire attempts to acquire a slot for the given endpoint.
	// Returns true if acquired, false if the domain is at capacity.
	// On Redis error, returns (true, nil) to fail-open.
	TryAcquire(ctx context.Context, endpoint string) (acquired bool, err error)

	// Release releases a slot for the given endpoint.
	// Should be deferred after a successful TryAcquire.
	Release(ctx context.Context, endpoint string) error
}

// ExtractDomain parses the hostname from an endpoint URL.
// If parsing fails, returns the raw endpoint string as fallback.
func ExtractDomain(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		// Fallback: return raw string, stripped of any path
		if idx := strings.Index(endpoint, "/"); idx > 0 {
			return endpoint[:idx]
		}
		return endpoint
	}
	return u.Host
}

// RedisDomainLimiter implements DomainLimiter using Redis.
type RedisDomainLimiter struct {
	client           *redis.Client
	defaultMax       int
	domainOverrides  map[string]int
	ttl              time.Duration
}

// decrDomainScript is a Lua script that decrements the domain counter but never goes below zero.
// This prevents counter drift from edge cases like duplicate processing.
var decrDomainScript = redis.NewScript(`
	local key = KEYS[1]
	local current = tonumber(redis.call('GET', key) or '0')
	if current > 0 then
		return redis.call('DECR', key)
	end
	return 0
`)

// NewRedisDomainLimiter creates a new Redis-backed domain limiter.
func NewRedisDomainLimiter(client *redis.Client, cfg config.ConcurrencyConfig) *RedisDomainLimiter {
	return &RedisDomainLimiter{
		client:          client,
		defaultMax:      cfg.MaxInflightPerDomain,
		domainOverrides: cfg.DomainOverrides,
		ttl:             cfg.InflightTTL,
	}
}

// TryAcquire attempts to acquire a slot for the given endpoint's domain.
// Returns true if acquired, false if domain is at capacity.
// On Redis error, returns (true, nil) to fail-open.
func (r *RedisDomainLimiter) TryAcquire(ctx context.Context, endpoint string) (bool, error) {
	domain := ExtractDomain(endpoint)
	key := domainConcurrencyPrefix + domain

	max := r.defaultMax
	if override, ok := r.domainOverrides[domain]; ok {
		max = override
	}

	// Use pipeline to INCR and EXPIRE atomically
	pipe := r.client.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, r.ttl)
	_, err := pipe.Exec(ctx)

	if err != nil {
		// Fail-open: allow request on Redis error
		log.Warn().Err(err).Str("domain", domain).Msg("Domain limiter INCR failed, failing open")
		return true, nil
	}

	count := incrCmd.Val()
	if count > int64(max) {
		// Over limit: decrement and reject
		if _, err := decrDomainScript.Run(ctx, r.client, []string{key}).Int64(); err != nil {
			log.Warn().Err(err).Str("domain", domain).Msg("Failed to decrement domain counter after reject")
		}
		return false, nil
	}

	return true, nil
}

// Release releases a slot for the given endpoint's domain.
func (r *RedisDomainLimiter) Release(ctx context.Context, endpoint string) error {
	domain := ExtractDomain(endpoint)
	key := domainConcurrencyPrefix + domain

	_, err := decrDomainScript.Run(ctx, r.client, []string{key}).Int64()
	if err != nil {
		return fmt.Errorf("decr domain concurrency: %w", err)
	}
	return nil
}

// NoopDomainLimiter is a no-op implementation that always allows requests.
// Used when domain limiting is disabled.
type NoopDomainLimiter struct{}

// NewNoopDomainLimiter creates a no-op domain limiter.
func NewNoopDomainLimiter() *NoopDomainLimiter {
	return &NoopDomainLimiter{}
}

// TryAcquire always returns (true, nil).
func (n *NoopDomainLimiter) TryAcquire(ctx context.Context, endpoint string) (bool, error) {
	return true, nil
}

// Release is a no-op.
func (n *NoopDomainLimiter) Release(ctx context.Context, endpoint string) error {
	return nil
}
