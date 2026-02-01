package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/circuit"
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

	// Load config
	cfg, err := config.LoadConfig(*configPath)
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

	// Initialize Redis hot state
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker, cfg.TTL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Initialize circuit breaker
	cb := circuit.NewBreaker(hs.Client(), cfg.CircuitBreaker, cfg.TTL)

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

	// Initialize script engine (pass Redis client for KV module and crypto module)
	scriptLoader := script.NewLoader()
	scriptEngine := script.NewEngine(cfg.Lua, scriptLoader, cryptoModule, hs.Client())
	defer scriptEngine.Close()

	// Create worker
	w := worker.NewWorker(cfg, kafkaBroker, publisher, hs, cb, dc, rs, scriptEngine)

	// Run worker
	go func() {
		if err := w.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Worker error")
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-sigCh
	log.Info().Msg("Shutdown signal received")
	cancel()
}
