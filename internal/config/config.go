package config

import (
	"time"
)

// Config is the root configuration
type Config struct {
	API            APIConfig            `mapstructure:"api"`
	Kafka          KafkaConfig          `mapstructure:"kafka"`
	Redis          RedisConfig          `mapstructure:"redis"`
	Delivery       DeliveryConfig       `mapstructure:"delivery"`
	Retry          RetryConfig          `mapstructure:"retry"`
	CircuitBreaker CircuitBreakerConfig `mapstructure:"circuit_breaker"`
	Scheduler      SchedulerConfig      `mapstructure:"scheduler"`
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
	ProducerBatchSize   int           `mapstructure:"producer_batch_size"`
	ProducerLingerMs    int           `mapstructure:"producer_linger_ms"`
	ProducerCompression string        `mapstructure:"producer_compression"` // none, gzip, snappy, lz4, zstd

	// Consumer settings
	ConsumerFetchMinBytes int `mapstructure:"consumer_fetch_min_bytes"`
	ConsumerFetchMaxWait  time.Duration `mapstructure:"consumer_fetch_max_wait"`
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
	Timeout            time.Duration `mapstructure:"timeout"`
	MaxIdleConns       int           `mapstructure:"max_idle_conns"`
	MaxConnsPerHost    int           `mapstructure:"max_conns_per_host"`
	IdleConnTimeout    time.Duration `mapstructure:"idle_conn_timeout"`
	TLSHandshakeTimeout time.Duration `mapstructure:"tls_handshake_timeout"`
	UserAgent          string        `mapstructure:"user_agent"`
}

type RetryConfig struct {
	BaseDelay   time.Duration `mapstructure:"base_delay"`
	MaxDelay    time.Duration `mapstructure:"max_delay"`
	MaxAttempts int           `mapstructure:"max_attempts"`
	Jitter      float64       `mapstructure:"jitter"` // 0.0 - 1.0
}

type CircuitBreakerConfig struct {
	FailureThreshold  int           `mapstructure:"failure_threshold"`   // failures before OPEN
	FailureWindow     time.Duration `mapstructure:"failure_window"`      // window for counting failures
	RecoveryTimeout   time.Duration `mapstructure:"recovery_timeout"`    // time before HALF_OPEN
	ProbeInterval     time.Duration `mapstructure:"probe_interval"`      // time between probes
	SuccessThreshold  int           `mapstructure:"success_threshold"`   // successes to close circuit
}

type SchedulerConfig struct {
	PollInterval  time.Duration `mapstructure:"poll_interval"`
	BatchSize     int           `mapstructure:"batch_size"`
	LockTimeout   time.Duration `mapstructure:"lock_timeout"`
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
			Brokers:             []string{"localhost:9092"},
			Topics: TopicsConfig{
				Pending:    "howk.pending",
				Results:    "howk.results",
				DeadLetter: "howk.deadletter",
			},
			ConsumerGroup:       "howk-workers",
			Retention:           7 * 24 * time.Hour, // 7 days
			ProducerBatchSize:   16384,
			ProducerLingerMs:    50,
			ProducerCompression: "snappy",
			ConsumerFetchMinBytes: 1,
			ConsumerFetchMaxWait:  500 * time.Millisecond,
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
			Timeout:            30 * time.Second,
			MaxIdleConns:       100,
			MaxConnsPerHost:    10,
			IdleConnTimeout:    90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
			UserAgent:          "HOWK/1.0",
		},
		Retry: RetryConfig{
			BaseDelay:   10 * time.Second,
			MaxDelay:    24 * time.Hour,
			MaxAttempts: 20,
			Jitter:      0.2,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold:  5,
			FailureWindow:     60 * time.Second,
			RecoveryTimeout:   5 * time.Minute,
			ProbeInterval:     60 * time.Second,
			SuccessThreshold:  2,
		},
		Scheduler: SchedulerConfig{
			PollInterval: 1 * time.Second,
			BatchSize:    500,
			LockTimeout:  30 * time.Second,
		},
	}
}
