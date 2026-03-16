package main

import (
	"flag"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/app"
	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/metrics"
	"github.com/howk/howk/internal/scheduler"
)

func main() {
	// Parse command-line flags
	configPath := flag.String("config", "", "Path to config file (optional)")
	flag.Parse()

	// Bootstrap: logging + config (also checks HOWK_CONFIG env var)
	cfg, err := app.Bootstrap(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Register Prometheus metrics with the default global registry.
	// Explicit registration avoids duplicate-registration panics in tests.
	metrics.Register(nil)

	// Setup context and signal handler
	ctx, cancel := app.SetupSignalHandler()
	defer cancel()

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

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go metrics.ListenAndServe(ctx, cfg.Metrics.Port)
	}

	// Create scheduler
	sched := scheduler.NewScheduler(cfg.Scheduler, hs, publisher)

	// Run scheduler
	go func() {
		if err := sched.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Scheduler error")
			cancel()
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
}
