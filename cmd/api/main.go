package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/api"
	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/metrics"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/script/modules"
	"github.com/howk/howk/internal/transformer"
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
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// Initialize Kafka broker
	kafkaBroker, err := broker.NewKafkaBroker(cfg.Kafka)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Kafka broker")
	}
	defer kafkaBroker.Close()

	// Initialize Kafka publisher
	publisher := broker.NewKafkaWebhookPublisher(kafkaBroker, cfg.Kafka.Topics)

	// Initialize Redis hot state
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker, cfg.TTL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Initialize script components
	scriptValidator := script.NewValidator()
	scriptPublisher := script.NewPublisher(kafkaBroker, cfg.Kafka.Topics.Scripts)

	// Initialize transformer feature (if enabled)
	var transformerRegistry *transformer.Registry
	var transformerEngine *transformer.Engine

	if cfg.Transformer.Enabled && len(cfg.Transformer.ScriptDirs) > 0 {
		log.Info().
			Strs("script_dirs", cfg.Transformer.ScriptDirs).
			Msg("Initializing transformer feature")

		// Create registry and load scripts
		transformerRegistry = transformer.NewRegistry(cfg.Transformer)
		if err := transformerRegistry.Load(); err != nil {
			log.Fatal().Err(err).Msg("Failed to load transformer scripts")
		}

		// Create optional modules for transformer engine
		var httpMod *modules.HTTPModule
		if cfg.Lua.HTTPTimeout > 0 {
			httpMod = modules.NewHTTPModule(modules.HTTPConfig{
				Timeout:             cfg.Lua.HTTPTimeout,
				GlobalAllowlist:     cfg.Lua.AllowedHosts,
				NamespaceAllowlists: cfg.Lua.AllowHostsByNamespace,
				CacheEnabled:        cfg.Lua.HTTPCacheEnabled,
				CacheTTL:            cfg.Lua.HTTPCacheTTL,
			})
		}

		var cryptoMod *modules.CryptoModule
		if len(cfg.Lua.CryptoKeys) > 0 {
			cryptoMod, err = modules.NewCryptoModule()
			if err != nil {
				log.Error().Err(err).Msg("Failed to create crypto module for transformer")
				// Non-fatal: transformer can work without crypto
			}
		}

		// Create transformer engine
		transformerEngine = transformer.NewEngine(
			cfg.Transformer,
			cfg.Lua,
			publisher,
			hs,
			hs.Client(),
			httpMod,
			cryptoMod,
		)

		log.Info().
			Int("scripts_loaded", len(transformerRegistry.List())).
			Msg("Transformer feature initialized")
	}

	// Create server options
	serverOpts := []api.ServerOption{}
	if transformerRegistry != nil && transformerEngine != nil {
		serverOpts = append(serverOpts, api.WithTransformers(transformerRegistry, transformerEngine))
	}

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go metrics.ListenAndServe(ctx, cfg.Metrics.Port)
	}

	// Create and run API server
	server := api.NewServer(cfg.API, publisher, hs, scriptValidator, scriptPublisher, serverOpts...)

	// Run server
	go func() {
		if err := server.Run(ctx); err != nil {
			log.Error().Err(err).Msg("API server error")
			cancel()
		}
	}()

	// Wait for shutdown signal
	for {
		sig := <-sigCh
		switch sig {
		case syscall.SIGHUP:
			// Hot reload transformer scripts
			if transformerRegistry != nil {
				log.Info().Msg("Received SIGHUP, reloading transformer scripts")
				if err := transformerRegistry.Reload(); err != nil {
					log.Error().Err(err).Msg("Failed to reload transformer scripts")
				} else {
					log.Info().
						Int("scripts_loaded", len(transformerRegistry.List())).
						Msg("Transformer scripts reloaded")
				}
			}
		case syscall.SIGINT, syscall.SIGTERM:
			log.Info().Str("signal", sig.String()).Msg("Shutdown signal received")
			cancel()
			return
		}
	}
}
