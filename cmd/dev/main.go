package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/api"
	"github.com/howk/howk/internal/app"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/delivery"
	"github.com/howk/howk/internal/devmode"
	"github.com/howk/howk/internal/hotstate"
	"github.com/howk/howk/internal/metrics"
	"github.com/howk/howk/internal/retry"
	"github.com/howk/howk/internal/script"
	"github.com/howk/howk/internal/script/modules"
	"github.com/howk/howk/internal/scheduler"
	"github.com/howk/howk/internal/transformer"
	"github.com/howk/howk/internal/worker"
)

func main() {
	configPath := flag.String("config", "", "Path to config file (optional)")
	scriptsDir := flag.String("scripts-dir", "", "Directory with .lua/.json script files")
	port := flag.Int("port", 0, "API port (overrides config, default 8080)")
	dry := flag.Bool("dry", false, "Dry-run mode: log deliveries without sending HTTP requests")
	flag.Parse()

	// Bootstrap config
	cfg, err := app.Bootstrap(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Dev-mode overrides
	cfg.Scheduler.PollInterval = 1 * time.Second
	if *port > 0 {
		cfg.API.Port = *port
	}
	if *scriptsDir != "" {
		cfg.Lua.Enabled = true
	}

	metrics.Register(nil)

	ctx, cancel := app.SetupSignalHandler()
	defer cancel()

	// --- Embedded Redis via miniredis ---
	mr, err := miniredis.Run()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to start miniredis")
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	hs, err := hotstate.NewRedisHotState(
		config.RedisConfig{Addr: mr.Addr()},
		cfg.CircuitBreaker,
		cfg.TTL,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create hotstate over miniredis")
	}
	defer hs.Close()

	// --- In-memory Kafka replacement ---
	memBroker := devmode.NewMemBroker()
	defer memBroker.Close()
	memPub := devmode.NewMemWebhookPublisher(memBroker, cfg.Kafka.Topics)

	// --- Script engine ---
	scriptLoader := script.NewLoader()

	if *scriptsDir != "" {
		n, err := devmode.LoadScriptsFromDir(*scriptsDir, scriptLoader)
		if err != nil {
			log.Fatal().Err(err).Str("dir", *scriptsDir).Msg("Failed to load scripts")
		}
		log.Info().Int("count", n).Str("dir", *scriptsDir).Msg("Loaded scripts from disk")
	}

	var cryptoModule *modules.CryptoModule
	if cfg.Lua.Enabled {
		cryptoModule, _ = modules.NewCryptoModule() // nil OK if no keys
	}

	var httpModule *modules.HTTPModule
	if cfg.Lua.Enabled {
		httpModule = modules.NewHTTPModule(modules.HTTPConfig{
			Timeout:             cfg.Lua.HTTPTimeout,
			GlobalAllowlist:     cfg.Lua.AllowedHosts,
			NamespaceAllowlists: cfg.Lua.AllowHostsByNamespace,
			CacheEnabled:        cfg.Lua.HTTPCacheEnabled,
			CacheTTL:            cfg.Lua.HTTPCacheTTL,
		})
	}

	scriptEngine := script.NewEngine(cfg.Lua, scriptLoader, cryptoModule, httpModule, rdb, log.Logger)
	defer scriptEngine.Close()

	// --- Delivery client ---
	var dc delivery.Deliverer
	if *dry {
		dc = devmode.NewDryDeliverer(log.Logger)
	} else {
		client := delivery.NewClient(cfg.Delivery)
		defer client.Close()
		dc = client
	}

	// --- Worker ---
	domainLimiter := delivery.NewNoopDomainLimiter()
	rs := retry.NewStrategy(cfg.Retry)

	workerOpts := []worker.WorkerOption{
		worker.WithDeliveryClient(dc),
		worker.WithRetryStrategy(rs),
		worker.WithScriptEngine(scriptEngine),
		worker.WithDomainLimiter(domainLimiter),
	}

	if cfg.Lua.Enabled {
		scriptConsumer := script.NewConsumer(
			memBroker,
			scriptLoader,
			hs,
			cfg.Kafka.Topics.Scripts,
			"dev-scripts",
			24*time.Hour,
		)
		workerOpts = append(workerOpts, worker.WithScriptConsumer(scriptConsumer))
	}

	w := worker.NewWorker(cfg, memBroker, memPub, hs, workerOpts...)

	go func() {
		if err := w.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Worker error")
			cancel()
		}
	}()

	slowWorker := worker.NewSlowWorker(w, memBroker, cfg)
	go func() {
		if err := slowWorker.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Slow worker error")
		}
	}()

	// --- Scheduler ---
	sched := scheduler.NewScheduler(cfg.Scheduler, hs, memPub)
	go func() {
		if err := sched.Run(ctx); err != nil {
			log.Error().Err(err).Msg("Scheduler error")
			cancel()
		}
	}()

	// --- API server ---
	scriptValidator := script.NewValidator()
	scriptPublisher := script.NewPublisher(memBroker, cfg.Kafka.Topics.Scripts)

	serverOpts := []api.ServerOption{}
	if cfg.Transformer.Enabled && len(cfg.Transformer.ScriptDirs) > 0 {
		transformerRegistry := transformer.NewRegistry(cfg.Transformer)
		if err := transformerRegistry.Load(); err != nil {
			log.Fatal().Err(err).Msg("Failed to load transformer scripts")
		}
		transformerEngine := transformer.NewEngine(
			cfg.Transformer, cfg.Lua, memPub, hs, rdb, httpModule, cryptoModule,
		)
		serverOpts = append(serverOpts, api.WithTransformers(transformerRegistry, transformerEngine))
	}

	server := api.NewServer(cfg.API, memPub, hs, scriptValidator, scriptPublisher, serverOpts...)

	// --- Banner ---
	mode := "live"
	if *dry {
		mode = "dry-run"
	}
	scriptsLoaded := "none"
	if *scriptsDir != "" {
		scriptsLoaded = *scriptsDir
	}

	banner := fmt.Sprintf(`
  ┌─────────────────────────────────────────┐
  │  HOWK Dev Mode                          │
  │  API:     http://localhost:%d          │
  │  Mode:    %-33s│
  │  Scripts: %-33s│
  │  KV:      enabled (miniredis)           │
  │  State:   in-memory (lost on restart)   │
  └─────────────────────────────────────────┘`, cfg.API.Port, mode, scriptsLoaded)

	log.Info().Msg(banner)

	// Run API server (blocking)
	go func() {
		if err := server.Run(ctx); err != nil {
			log.Error().Err(err).Msg("API server error")
			cancel()
		}
	}()

	<-ctx.Done()

	// Graceful shutdown
	if w.GetScriptConsumer() != nil {
		if err := w.GetScriptConsumer().Stop(); err != nil {
			log.Warn().Err(err).Msg("Error stopping script consumer")
		}
	}

}
