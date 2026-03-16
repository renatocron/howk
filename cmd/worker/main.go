package main

import (
	"flag"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/app"
	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/metrics"
	"github.com/howk/howk/internal/retry"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/script/modules"
	"github.com/howk/howk/internal/worker"
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

	// Initialize Redis hot state (includes circuit breaker)
	hs, err := hotstate.NewRedisHotState(cfg.Redis, cfg.CircuitBreaker, cfg.TTL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Redis hot state")
	}
	defer hs.Close()

	// Initialize delivery client
	dc := delivery.NewClient(cfg.Delivery)
	defer dc.Close()

	// Initialize domain limiter (if enabled)
	var domainLimiter delivery.DomainLimiter
	if cfg.Concurrency.MaxInflightPerDomain > 0 {
		domainLimiter = delivery.NewRedisDomainLimiter(hs.Client(), cfg.Concurrency)
		log.Info().
			Int("max_inflight_per_domain", cfg.Concurrency.MaxInflightPerDomain).
			Interface("domain_overrides", cfg.Concurrency.DomainOverrides).
			Msg("Domain concurrency limiter enabled")

		// Warn if HTTP transport would bottleneck
		if cfg.Concurrency.MaxInflightPerDomain > cfg.Delivery.MaxConnsPerHost {
			log.Warn().
				Int("max_inflight_per_domain", cfg.Concurrency.MaxInflightPerDomain).
				Int("max_conns_per_host", cfg.Delivery.MaxConnsPerHost).
				Msg("MaxInflightPerDomain exceeds MaxConnsPerHost - HTTP transport may bottleneck")
		}
	} else {
		domainLimiter = delivery.NewNoopDomainLimiter()
		log.Debug().Msg("Domain concurrency limiter disabled (MaxInflightPerDomain = 0)")
	}

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

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go metrics.ListenAndServe(ctx, cfg.Metrics.Port)
	}

	// Build worker options. Script consumer is wired at construction time via
	// WithScriptConsumer to avoid fragile post-construction setter calls.
	var workerOpts []worker.WorkerOption
	if cfg.Lua.Enabled {
		scriptConsumer := script.NewConsumer(
			kafkaBroker,
			scriptLoader,
			hs,
			cfg.Kafka.Topics.Scripts,
			cfg.Kafka.ConsumerGroup+"-scripts", // Separate consumer group for scripts
			24*time.Hour,                       // Script cache TTL
		)
		workerOpts = append(workerOpts, worker.WithScriptConsumer(scriptConsumer))
	}

	// Create worker with all dependencies resolved at construction time
	w := worker.NewWorker(cfg, kafkaBroker, publisher, hs, dc, rs, scriptEngine, domainLimiter, workerOpts...)

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
	<-ctx.Done()

	// Stop script consumer gracefully
	if w.GetScriptConsumer() != nil {
		if err := w.GetScriptConsumer().Stop(); err != nil {
			log.Warn().Err(err).Msg("Error stopping script consumer")
		}
	}
}
