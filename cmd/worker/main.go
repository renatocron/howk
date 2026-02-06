package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/retry"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/script/modules"
	"github.com/howk/howk/internal/worker"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("config", "", "Path to config file (optional)")
	flag.Parse()

	// Setup logging
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Determine config path: flag > env var > empty (uses defaults)
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("HOWK_CONFIG")
	}

	// Load config
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Initialize Kafka broker
	kafkaBroker, err := broker.NewKafkaBroker(cfg.Kafka)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Kafka broker")
	}
	defer kafkaBroker.Close()

	// Initialize Kafka publisher
	publisher := broker.NewKafkaWebhookPublisher(kafkaBroker, cfg.Kafka.Topics)

	// Initialize Redis hot state (includes circuit breaker)
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker, cfg.TTL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Initialize delivery client
	dc := delivery.NewClient(cfg.Delivery)
	defer dc.Close()

	// Initialize retry strategy
	rs := retry.NewStrategy(cfg.Retry)

	// Initialize crypto module (loads keys from HOWK_LUA_CRYPTO_* env vars)
	var cryptoModule *modules.CryptoModule
	if cfg.Lua.Enabled {
		cryptoModule, err = modules.NewCryptoModule()
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to load crypto module")
		}
		if cryptoModule.KeyCount() > 0 {
			log.Info().Int("key_count", cryptoModule.KeyCount()).Msg("Crypto module loaded")
		}
	}

	// Initialize HTTP module with allowlist configuration
	var httpModule *modules.HTTPModule
	if cfg.Lua.Enabled {
		httpModule = modules.NewHTTPModule(modules.HTTPConfig{
			Timeout:             cfg.Lua.HTTPTimeout,
			GlobalAllowlist:     cfg.Lua.AllowedHosts,
			NamespaceAllowlists: cfg.Lua.AllowHostsByNamespace,
			CacheEnabled:        cfg.Lua.HTTPCacheEnabled,
			CacheTTL:            cfg.Lua.HTTPCacheTTL,
		})
		log.Info().
			Bool("cache_enabled", cfg.Lua.HTTPCacheEnabled).
			Dur("cache_ttl", cfg.Lua.HTTPCacheTTL).
			Msg("HTTP module loaded")
	}

	// Initialize script engine (pass Redis client, crypto module, HTTP module, and logger)
	scriptLoader := script.NewLoader()
	scriptEngine := script.NewEngine(cfg.Lua, scriptLoader, cryptoModule, httpModule, hs.Client(), log.Logger)
	defer scriptEngine.Close()

	// Create worker
	w := worker.NewWorker(cfg, kafkaBroker, publisher, hs, dc, rs, scriptEngine)

	// Initialize and start script consumer if Lua is enabled
	// This ensures the worker has scripts even if Redis is flushed
	if cfg.Lua.Enabled {
		scriptConsumer := script.NewScriptConsumer(
			kafkaBroker,
			scriptLoader,
			hs,
			cfg.Kafka.Topics.Scripts,
			cfg.Kafka.ConsumerGroup+"-scripts", // Separate consumer group for scripts
			24*time.Hour,                      // Script cache TTL
		)
		w.SetScriptConsumer(scriptConsumer)

		// Start script consumer in background
		if err := scriptConsumer.Start(ctx); err != nil {
			log.Error().Err(err).Msg("Failed to start script consumer, continuing with lazy loading only")
		} else {
			log.Info().
				Str("topic", cfg.Kafka.Topics.Scripts).
				Str("consumer_group", cfg.Kafka.ConsumerGroup+"-scripts").
				Msg("Script consumer started - scripts will be synchronized from Kafka")
		}
	}

	// Run worker
	go func() {
		if err := w.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Worker error")
			cancel()
		}
	}()

	// Start slow worker goroutine
	slowWorker := worker.NewSlowWorker(w, kafkaBroker, cfg)
	go func() {
		if err := slowWorker.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Slow worker error")
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Info().Msg("Shutdown signal received")
	cancel()

	// Stop script consumer gracefully
	if w.GetScriptConsumer() != nil {
		if err := w.GetScriptConsumer().Stop(); err != nil {
			log.Warn().Err(err).Msg("Error stopping script consumer")
		}
	}
}
