package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/scheduler"
)

func main() {
	// Setup logging
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Load config
	cfg := config.DefaultConfig()

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
	hs, err := hotstate.NewRedisHotState(cfg.Redis)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

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
	<-sigCh
	log.Info().Msg("Shutdown signal received")
	cancel()
}
