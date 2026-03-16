// Package app provides common bootstrap utilities for HOWK services
package app

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
)

// Bootstrap initializes common application components and returns the loaded
// configuration. configPath may be empty; if so, Bootstrap falls back to the
// HOWK_CONFIG environment variable and then to built-in defaults.
func Bootstrap(configPath string) (*config.Config, error) {
	// Setup logging
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Resolve config path: explicit flag > HOWK_CONFIG env var > defaults
	cfgPath := configPath
	if cfgPath == "" {
		cfgPath = os.Getenv("HOWK_CONFIG")
	}

	// Load config
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// SetupSignalHandler creates a context that is cancelled when a shutdown signal is received
// Returns the context and cancel function
func SetupSignalHandler() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Info().Msg("Shutdown signal received")
		cancel()
	}()

	return ctx, cancel
}
