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

// ScriptConsumer consumes script configurations from Kafka and maintains a local cache
// This ensures the Worker has scripts even if Redis is flushed
type ScriptConsumer struct {
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

// NewScriptConsumer creates a new script consumer
func NewScriptConsumer(brk broker.Broker, loader *Loader, hs ScriptStore, topic, group string, cacheTTL time.Duration) *ScriptConsumer {
	if cacheTTL == 0 {
		cacheTTL = 24 * time.Hour // Default TTL
	}
	return &ScriptConsumer{
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
func (c *ScriptConsumer) Start(ctx context.Context) error {
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
func (c *ScriptConsumer) Stop() error {
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
func (c *ScriptConsumer) run(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		default:
		}

		// Subscribe and consume
		err := c.broker.Subscribe(ctx, c.topic, c.group, c.handleMessage)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error().Err(err).Msg("Script consumer subscription error, retrying...")
			time.Sleep(time.Second)
		}
	}
}

// handleMessage processes a single script message
func (c *ScriptConsumer) handleMessage(ctx context.Context, msg *broker.Message) error {
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
	var script ScriptConfig
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
func (c *ScriptConsumer) IsRunning() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.running
}

// PrefetchScripts loads all scripts from Redis into the loader cache
// This should be called on startup before starting the consumer
func (c *ScriptConsumer) PrefetchScripts(ctx context.Context) error {
	// This would require a method to list all scripts in Redis
	// For now, scripts are loaded lazily when webhooks are processed
	// The reconciler can be used to rebuild script state from Kafka if needed
	return nil
}
