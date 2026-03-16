package main

import (
	"context"
	"flag"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/app"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/reconciler"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "", "Path to config file (optional)")
	flag.Parse()

	// Bootstrap: logging + config (also checks HOWK_CONFIG env var)
	cfg, err := app.Bootstrap(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Setup context and signal handler
	ctx, cancel := app.SetupSignalHandler()
	defer cancel()

	// Initialize Redis hot state
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker, cfg.TTL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Create and run reconciler
	rec := reconciler.NewReconciler(cfg.Kafka, hs, cfg.TTL)

	if err := rec.Run(ctx); err != nil {
		if err == context.Canceled {
			log.Info().Msg("Reconciliation cancelled")
		} else {
			log.Fatal().Err(err).Msg("Reconciliation failed")
		}
	}
}
