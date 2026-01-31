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
}

type TopicsConfig struct {
	Pending    string `mapstructure:"pending"`
	Results    string `mapstructure:"results"`
	DeadLetter string `mapstructure:"deadletter"`
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
	}

	for _, key := range keys {
		if err := v.BindEnv(key); err != nil {
			return fmt.Errorf("error binding env var for key %s: %w", key, err)
		}
	}

	return nil
}
