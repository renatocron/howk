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

// Bootstrap initializes common application components
// Returns the loaded configuration or an error
func Bootstrap(configPath string) (*config.Config, error) {
	// Setup logging
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	zerolog.SetGlobalLevel(zerolog.InfoLevel)

	// Load config
	cfg, err := config.LoadConfig(configPath)
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
