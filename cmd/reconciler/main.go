package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/reconciler"
)

func main() {
	// Parse flags
	fromBeginning := flag.Bool("from-beginning", false, "Start reconciliation from the beginning of the Kafka topic")
	flag.Parse()

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
	go func() {
		<-sigCh
		log.Info().Msg("Shutdown signal received")
		cancel()
	}()

	// Initialize Redis hot state
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Create and run reconciler
	rec := reconciler.NewReconciler(cfg.Kafka, hs)

	if err := rec.Run(ctx, *fromBeginning); err != nil {
		if err == context.Canceled {
			log.Info().Msg("Reconciliation cancelled")
		} else {
			log.Fatal().Err(err).Msg("Reconciliation failed")
		}
	}
}
