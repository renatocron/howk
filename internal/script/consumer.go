package script

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
)

// ScriptStore interface for hotstate operations
type ScriptStore interface {
	GetScript(ctx context.Context, configID domain.ConfigID) (string, error)
	SetScript(ctx context.Context, configID domain.ConfigID, scriptJSON string, ttl time.Duration) error
	DeleteScript(ctx context.Context, configID domain.ConfigID) error
}

// Consumer replays script configurations from the compacted Kafka topic and
// maintains a local in-memory cache (Loader), write-through to Redis.
// Every instance holds the FULL script state regardless of replica count, so
// scripts survive Redis loss/TTL expiry and pod restarts. The group parameter
// is retained for logging/identification only — Replay does not use consumer
// groups.
type Consumer struct {
	broker    broker.Broker
	loader    *Loader
	hotstate  ScriptStore
	topic     string
	group     string
	cacheTTL  time.Duration

	mu        sync.RWMutex
	running   bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewConsumer creates a new script consumer.
//
// cacheTTL is the TTL for the Redis write-through mirror of each script.
// A value <= 0 means NO EXPIRY (persist): the compacted Kafka topic is the
// source of truth and deletes are propagated via tombstones, so there is no
// reason for the Redis mirror to expire on its own — an expiring mirror is
// exactly what silently disabled all transformation once (see the v0.4.9
// post-mortem). Pass 0 unless you have a specific reason to expire.
func NewConsumer(brk broker.Broker, loader *Loader, hs ScriptStore, topic, group string, cacheTTL time.Duration) *Consumer {
	// Clamp negatives to 0: go-redis interprets -1 (redis.KeepTTL) as
	// "preserve the key's existing TTL", which on a key still carrying the
	// legacy 7-day TTL would re-arm the exact expiry incident this exists to
	// prevent. Only exactly-0 yields a plain SET with no expiration.
	if cacheTTL < 0 {
		cacheTTL = 0
	}
	return &Consumer{
		broker:   brk,
		loader:   loader,
		hotstate: hs,
		topic:    topic,
		group:    group,
		cacheTTL: cacheTTL,
		stopCh:   make(chan struct{}),
	}
}

// Start begins consuming scripts from Kafka in a background goroutine
func (c *Consumer) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running {
		return fmt.Errorf("script consumer already running")
	}

	c.running = true
	c.wg.Add(1)

	go c.run(ctx)

	log.Info().
		Str("topic", c.topic).
		Str("group", c.group).
		Msg("Script consumer started")

	return nil
}

// Stop gracefully stops the script consumer
func (c *Consumer) Stop() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	close(c.stopCh)
	c.mu.Unlock()

	// Wait for consumer to finish
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info().Msg("Script consumer stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Warn().Msg("Script consumer stop timeout")
	}

	return nil
}

// run is the main consumer loop
func (c *Consumer) run(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		default:
		}

		// Replay the compacted topic from the earliest offset (no consumer
		// group): every instance materializes the FULL script state in memory.
		// The old group-based Subscribe had two fatal properties for a state
		// topic: partitions were split across replicas (each pod saw only a
		// subset of scripts), and a fresh group started at OffsetNewest, so a
		// restarted pod never saw previously-deployed scripts at all — which,
		// combined with the Redis script TTL, silently disabled transformation.
		err := c.broker.Replay(ctx, c.topic, c.handleMessage)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("Script consumer replay error, retrying...")
			time.Sleep(time.Second)
		}
	}
}

// handleMessage processes a single script message
func (c *Consumer) handleMessage(ctx context.Context, msg *broker.Message) error {
	configID := string(msg.Key)

	// Tombstone message (null value) - delete the script
	if msg.Value == nil || len(msg.Value) == 0 {
		log.Info().Str("config_id", configID).Msg("Received script tombstone, deleting")
		
		// Delete from loader cache
		c.loader.DeleteScript(domain.ConfigID(configID))
		
		// Delete from Redis if available
		if c.hotstate != nil {
			if err := c.hotstate.DeleteScript(ctx, domain.ConfigID(configID)); err != nil {
				log.Warn().Err(err).Str("config_id", configID).Msg("Failed to delete script from Redis")
			}
		}
		
		return nil
	}

	// Parse script configuration
	var script Config
	if err := json.Unmarshal(msg.Value, &script); err != nil {
		log.Error().Err(err).Str("config_id", configID).Msg("Failed to unmarshal script config")
		return fmt.Errorf("unmarshal script config: %w", err)
	}

	// Basic validation
	if script.ConfigID == "" {
		log.Error().Str("config_id", configID).Msg("Script config missing ConfigID")
		return fmt.Errorf("script config missing ConfigID")
	}
	if script.LuaCode == "" {
		log.Error().Str("config_id", configID).Msg("Script config missing LuaCode")
		return fmt.Errorf("script config missing LuaCode")
	}

	// Store in loader cache
	c.loader.SetScript(&script)

	// Store in Redis for other workers if available
	if c.hotstate != nil {
		scriptJSON, err := json.Marshal(script)
		if err != nil {
			log.Warn().Err(err).Str("config_id", configID).Msg("Failed to marshal script for Redis")
		} else {
			if err := c.hotstate.SetScript(ctx, domain.ConfigID(configID), string(scriptJSON), c.cacheTTL); err != nil {
				log.Warn().Err(err).Str("config_id", configID).Msg("Failed to store script in Redis")
			}
		}
	}

	log.Debug().
		Str("config_id", configID).
		Str("script_hash", script.Hash).
		Str("version", script.Version).
		Msg("Script loaded from Kafka")

	return nil
}

// IsRunning returns whether the consumer is currently running
func (c *Consumer) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// PrefetchScripts loads all scripts from Redis into the loader cache
// This should be called on startup before starting the consumer
func (c *Consumer) PrefetchScripts(ctx context.Context) error {
	// This would require a method to list all scripts in Redis
	// For now, scripts are loaded lazily when webhooks are processed
	// The reconciler can be used to rebuild script state from Kafka if needed
	return nil
}
