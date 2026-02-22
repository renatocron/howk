package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// TTLConfig configures TTLs for various Redis keys
type TTLConfig struct {
	CircuitStateTTL time.Duration `mapstructure:"circuit_state_ttl"`
	StatusTTL       time.Duration `mapstructure:"status_ttl"`
	StatsTTL        time.Duration `mapstructure:"stats_ttl"`
	IdempotencyTTL  time.Duration `mapstructure:"idempotency_ttl"`
	RetryDataTTL    time.Duration `mapstructure:"retry_data_ttl"` // NEW: TTL for compressed retry data
}

// Config is the root configuration
type Config struct {
	API            APIConfig            `mapstructure:"api"`
	Kafka          KafkaConfig          `mapstructure:"kafka"`
	Redis          RedisConfig          `mapstructure:"redis"`
	Delivery       DeliveryConfig       `mapstructure:"delivery"`
	Retry          RetryConfig          `mapstructure:"retry"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	Scheduler      SchedulerConfig      `mapstructure:"scheduler"`
	TTL            TTLConfig            `mapstructure:"ttl"`
	Lua            LuaConfig            `mapstructure:"lua"`
	Concurrency    ConcurrencyConfig    `mapstructure:"concurrency"`
	Transformer    TransformerConfig    `mapstructure:"transformer"`
	Metrics        MetricsConfig        `mapstructure:"metrics"`
}

type APIConfig struct {
	Port           int           `mapstructure:"port"`
	ReadTimeout    time.Duration `mapstructure:"read_timeout"`
	WriteTimeout   time.Duration `mapstructure:"write_timeout"`
	MaxRequestSize int64         `mapstructure:"max_request_size"`
}

type KafkaConfig struct {
	Brokers       []string      `mapstructure:"brokers"`
	Topics        TopicsConfig  `mapstructure:"topics"`
	ConsumerGroup string        `mapstructure:"consumer_group"`
	Retention     time.Duration `mapstructure:"retention"`

	// Producer settings
	ProducerBatchSize   int    `mapstructure:"producer_batch_size"`
	ProducerLingerMs    int    `mapstructure:"producer_linger_ms"`
	ProducerCompression string `mapstructure:"producer_compression"` // none, gzip, snappy, lz4, zstd

	// Consumer settings
	ConsumerFetchMinBytes int           `mapstructure:"consumer_fetch_min_bytes"`
	ConsumerFetchMaxWait  time.Duration `mapstructure:"consumer_fetch_max_wait"`

	// Consumer group settings
	GroupSessionTimeout    time.Duration `mapstructure:"group_session_timeout"`
	GroupHeartbeatInterval time.Duration `mapstructure:"group_heartbeat_interval"`
	GroupRebalanceTimeout  time.Duration `mapstructure:"group_rebalance_timeout"`

	// PerKeyParallelism controls how many goroutines process messages for the same
	// partition key (ConfigID) concurrently. Default: 1 (sequential, current behavior).
	// Higher values trade per-key ordering for throughput.
	PerKeyParallelism int `mapstructure:"per_key_parallelism"`
}

type TopicsConfig struct {
	Pending    string `mapstructure:"pending"`
	Results    string `mapstructure:"results"`
	DeadLetter string `mapstructure:"deadletter"`
	Scripts    string `mapstructure:"scripts"`
	Slow       string `mapstructure:"slow"`
	State      string `mapstructure:"state"` // Compacted topic for zero-maintenance reconciliation
}

type RedisConfig struct {
	Addr         string        `mapstructure:"addr"`
	Password     string        `mapstructure:"password"`
	DB           int           `mapstructure:"db"`
	PoolSize     int           `mapstructure:"pool_size"`
	MinIdleConns int           `mapstructure:"min_idle_conns"`
	DialTimeout  time.Duration `mapstructure:"dial_timeout"`
	ReadTimeout  time.Duration `mapstructure:"read_timeout"`
	WriteTimeout time.Duration `mapstructure:"write_timeout"`
}

type DeliveryConfig struct {
	Timeout             time.Duration `mapstructure:"timeout"`
	MaxIdleConns        int           `mapstructure:"max_idle_conns"`
	MaxConnsPerHost     int           `mapstructure:"max_conns_per_host"`
	IdleConnTimeout     time.Duration `mapstructure:"idle_conn_timeout"`
	TLSHandshakeTimeout time.Duration `mapstructure:"tls_handshake_timeout"`
	UserAgent           string        `mapstructure:"user_agent"`
}

type RetryConfig struct {
	BaseDelay   time.Duration `mapstructure:"base_delay"`
	MaxDelay    time.Duration `mapstructure:"max_delay"`
	MaxAttempts int           `mapstructure:"max_attempts"`
	Jitter      float64       `mapstructure:"jitter"` // 0.0 - 1.0
}

type CircuitBreakerConfig struct {
	FailureThreshold int           `mapstructure:"failure_threshold"` // failures before OPEN
	FailureWindow    time.Duration `mapstructure:"failure_window"`    // window for counting failures
	RecoveryTimeout  time.Duration `mapstructure:"recovery_timeout"`  // time before HALF_OPEN
	ProbeInterval    time.Duration `mapstructure:"probe_interval"`    // time between probes
	SuccessThreshold int           `mapstructure:"success_threshold"` // successes to close circuit
}

type SchedulerConfig struct {
	PollInterval time.Duration `mapstructure:"poll_interval"`
	BatchSize    int           `mapstructure:"batch_size"`
	LockTimeout  time.Duration `mapstructure:"lock_timeout"`
}

// ConcurrencyConfig configures the penalty box / slow lane behavior.
type ConcurrencyConfig struct {
	// MaxInflightPerEndpoint is the threshold above which webhooks are
	// diverted to the slow topic. Default: 50.
	MaxInflightPerEndpoint int `mapstructure:"max_inflight_per_endpoint"`

	// InflightTTL is the TTL for the concurrency counter key.
	// Acts as a safety net: if a worker crashes mid-delivery, the counter
	// will auto-expire and self-correct. Should be > delivery timeout.
	// Default: 2m (= 4x the 30s delivery timeout).
	InflightTTL time.Duration `mapstructure:"inflight_ttl"`

	// SlowLaneRate is the maximum number of deliveries per second
	// from the slow lane per worker instance. Default: 20.
	SlowLaneRate int `mapstructure:"slow_lane_rate"`

	// MaxInflightPerDomain is the default max concurrent requests to any single domain.
	// 0 = disabled (no domain-level limiting). Aggregates across all endpoint URLs on same host.
	MaxInflightPerDomain int `mapstructure:"max_inflight_per_domain"`

	// DomainOverrides maps specific domains to custom concurrency limits.
	// Example: {"api.stripe.com": 200, "hooks.slack.com": 30}
	DomainOverrides map[string]int `mapstructure:"domain_overrides"`
}

type LuaConfig struct {
	Enabled       bool              `mapstructure:"enabled"`
	Timeout       time.Duration     `mapstructure:"timeout"`
	MemoryLimitMB int               `mapstructure:"memory_limit_mb"`
	AllowedHosts  []string          `mapstructure:"allowed_hosts"`
	CryptoKeys    map[string]string `mapstructure:"crypto_keys"` // key_name -> path to PEM file
	HTTPTimeout   time.Duration     `mapstructure:"http_timeout"`
	KVTTLDefault  time.Duration     `mapstructure:"kv_ttl_default"`
	// Namespace-specific allowlists (key: namespace, value: comma-separated hosts)
	// Env var format: HOWK_LUA_ALLOW_HOSTNAME_{NAMESPACE}=host1,host2
	AllowHostsByNamespace map[string]string `mapstructure:"allow_hosts_by_namespace"`
	// HTTP response cache settings
	HTTPCacheEnabled bool          `mapstructure:"http_cache_enabled"`
	HTTPCacheTTL     time.Duration `mapstructure:"http_cache_ttl"`
}

// MetricsConfig configures the Prometheus metrics endpoint
type MetricsConfig struct {
	Enabled bool `mapstructure:"enabled"`
	Port    int  `mapstructure:"port"`
}

// TransformerConfig configures the API-side Lua transformer feature for incoming webhook fan-out
type TransformerConfig struct {
	Enabled       bool          `mapstructure:"enabled"`         // default: false
	ScriptDirs    []string      `mapstructure:"script_dirs"`     // e.g. ["/etc/howk/transformers/"]
	PasswdDirs    []string      `mapstructure:"passwd_dirs"`     // optional; if empty, uses ScriptDirs
	Timeout       time.Duration `mapstructure:"timeout"`         // default: 500ms
	MemoryLimitMB int           `mapstructure:"memory_limit_mb"` // default: 50
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	return &Config{
		API: APIConfig{
			Port:           8080,
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   10 * time.Second,
			MaxRequestSize: 1 << 20, // 1MB
		},
		Kafka: KafkaConfig{
			Brokers: []string{"localhost:9092"},
			Topics: TopicsConfig{
				Pending:    "howk.pending",
				Results:    "howk.results",
				DeadLetter: "howk.deadletter",
				Scripts:    "howk.scripts",
				Slow:       "howk.slow",
				State:      "howk.state",
			},
			ConsumerGroup:          "howk-workers",
			Retention:              7 * 24 * time.Hour, // 7 days
			ProducerBatchSize:      16384,
			ProducerLingerMs:       50,
			ProducerCompression:    "snappy",
			ConsumerFetchMinBytes:  1,
			ConsumerFetchMaxWait:   500 * time.Millisecond,
			GroupSessionTimeout:    10 * time.Second,
			GroupHeartbeatInterval: 3 * time.Second,
			GroupRebalanceTimeout:  10 * time.Second,
		},
		Redis: RedisConfig{
			Addr:         "localhost:6379",
			PoolSize:     100,
			MinIdleConns: 10,
			DialTimeout:  5 * time.Second,
			ReadTimeout:  3 * time.Second,
			WriteTimeout: 3 * time.Second,
		},
		Delivery: DeliveryConfig{
			Timeout:             30 * time.Second,
			MaxIdleConns:        100,
			MaxConnsPerHost:     10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			UserAgent:           "HOWK/1.0",
		},
		Retry: RetryConfig{
			BaseDelay:   10 * time.Second,
			MaxDelay:    24 * time.Hour,
			MaxAttempts: 20,
			Jitter:      0.2,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold: 5,
			FailureWindow:    60 * time.Second,
			RecoveryTimeout:  5 * time.Minute,
			ProbeInterval:    60 * time.Second,
			SuccessThreshold: 2,
		},
		Scheduler: SchedulerConfig{
			PollInterval: 1 * time.Second,
			BatchSize:    500,
			LockTimeout:  30 * time.Second,
		},
		TTL: TTLConfig{
			CircuitStateTTL: 24 * time.Hour,
			StatusTTL:       7 * 24 * time.Hour,
			StatsTTL:        48 * time.Hour,
			IdempotencyTTL:  24 * time.Hour,
		},
		Lua: LuaConfig{
			Enabled:               false, // Feature flag - must be explicitly enabled
			Timeout:               500 * time.Millisecond,
			MemoryLimitMB:         50,
			AllowedHosts:          []string{"*"}, // Allow all by default
			CryptoKeys:            map[string]string{},
			HTTPTimeout:           5 * time.Second,
			KVTTLDefault:          24 * time.Hour,
			AllowHostsByNamespace: map[string]string{},
			HTTPCacheEnabled:      true,
			HTTPCacheTTL:          5 * time.Minute,
		},
		Concurrency: ConcurrencyConfig{
			MaxInflightPerEndpoint: 50,
			InflightTTL:            2 * time.Minute,
			SlowLaneRate:           20,
			MaxInflightPerDomain:   0,
			DomainOverrides:        map[string]int{},
		},
		Transformer: TransformerConfig{
			Enabled:       false, // Feature flag - must be explicitly enabled
			ScriptDirs:    []string{},
			PasswdDirs:    []string{}, // Empty means use ScriptDirs
			Timeout:       500 * time.Millisecond,
			MemoryLimitMB: 50,
		},
		Metrics: MetricsConfig{
			Enabled: false, // Feature flag - must be explicitly enabled
			Port:    9090,
		},
	}
}

// LoadConfig loads configuration from environment variables, config file, and defaults
// Priority: environment variables > config file > defaults
func LoadConfig(configPath string) (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	v := viper.New()

	// Set environment variable prefix and key replacer
	v.SetEnvPrefix("HOWK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Search for config file in multiple locations if path not provided
	if configPath == "" {
		v.AddConfigPath(".")
		homeDir, _ := os.UserHomeDir()
		if homeDir != "" {
			v.AddConfigPath(filepath.Join(homeDir, ".howk"))
		}
		v.AddConfigPath("/etc/howk")
		v.SetConfigName("config")
		v.SetConfigType("yaml")

		// Try to read config file (non-fatal if missing)
		if err := v.ReadInConfig(); err != nil {
			// Only log if it's not a "not found" error
			if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
				return nil, fmt.Errorf("error reading config file: %w", err)
			}
			// Config file not found is OK, we'll use defaults + env vars
		}
	} else {
		// Use provided config file path
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("error reading config file %s: %w", configPath, err)
		}
	}

	// Enable reading from environment variables
	v.AutomaticEnv()

	// Explicitly bind all environment variables
	if err := bindEnvVariables(v); err != nil {
		return nil, err
	}

	// Unmarshal config, with defaults as fallback
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("error unmarshalling config: %w", err)
	}

	return cfg, nil
}

// bindEnvVariables explicitly binds all configuration keys to environment variables
func bindEnvVariables(v *viper.Viper) error {
	keys := []string{
		// API
		"api.port",
		"api.read_timeout",
		"api.write_timeout",
		"api.max_request_size",
		// Kafka
		"kafka.brokers",
		"kafka.topics.pending",
		"kafka.topics.results",
		"kafka.topics.deadletter",
		"kafka.topics.scripts",
		"kafka.consumer_group",
		"kafka.retention",
		"kafka.producer_batch_size",
		"kafka.producer_linger_ms",
		"kafka.producer_compression",
		"kafka.consumer_fetch_min_bytes",
		"kafka.consumer_fetch_max_wait",
		"kafka.group_session_timeout",
		"kafka.group_heartbeat_interval",
		"kafka.group_rebalance_timeout",
		// Redis
		"redis.addr",
		"redis.password",
		"redis.db",
		"redis.pool_size",
		"redis.min_idle_conns",
		"redis.dial_timeout",
		"redis.read_timeout",
		"redis.write_timeout",
		// Delivery
		"delivery.timeout",
		"delivery.max_idle_conns",
		"delivery.max_conns_per_host",
		"delivery.idle_conn_timeout",
		"delivery.tls_handshake_timeout",
		"delivery.user_agent",
		// Retry
		"retry.base_delay",
		"retry.max_delay",
		"retry.max_attempts",
		"retry.jitter",
		// Circuit Breaker
		"circuit_breaker.failure_threshold",
		"circuit_breaker.failure_window",
		"circuit_breaker.recovery_timeout",
		"circuit_breaker.probe_interval",
		"circuit_breaker.success_threshold",
		// Scheduler
		"scheduler.poll_interval",
		"scheduler.batch_size",
		"scheduler.lock_timeout",
		// TTL
		"ttl.circuit_state_ttl",
		"ttl.status_ttl",
		"ttl.stats_ttl",
		"ttl.idempotency_ttl",
		"ttl.retry_data_ttl",
		// Lua
		"lua.enabled",
		"lua.timeout",
		"lua.memory_limit_mb",
		"lua.allowed_hosts",
		"lua.crypto_keys",
		"lua.http_timeout",
		"lua.kv_ttl_default",
		"lua.allow_hosts_by_namespace",
		"lua.http_cache_enabled",
		"lua.http_cache_ttl",
		// Concurrency / Slow Lane
		"kafka.topics.slow",
		"kafka.topics.state",
		"kafka.per_key_parallelism",
		"concurrency.max_inflight_per_endpoint",
		"concurrency.inflight_ttl",
		"concurrency.slow_lane_rate",
		"concurrency.max_inflight_per_domain",
		"concurrency.domain_overrides",
		// Transformer
		"transformer.enabled",
		"transformer.script_dirs",
		"transformer.passwd_dirs",
		"transformer.timeout",
		"transformer.memory_limit_mb",
		// Metrics
		"metrics.enabled",
		"metrics.port",
	}

	for _, key := range keys {
		if err := v.BindEnv(key); err != nil {
			return fmt.Errorf("error binding env var for key %s: %w", key, err)
		}
	}

	return nil
}
