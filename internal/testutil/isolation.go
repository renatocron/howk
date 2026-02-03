//go:build integration

package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/hotstate"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// IsolatedEnv provides isolated Kafka topics and Redis keys for a single test.
// All resources are cleaned up automatically via t.Cleanup().
type IsolatedEnv struct {
	// Config returns a config.Config with isolated topic names and consumer group.
	// The returned config can be passed directly to component constructors.
	Config *config.Config

	// Broker is a ready-to-use Kafka broker instance.
	Broker *broker.KafkaBroker

	// HotState is a ready-to-use Redis hot state with isolated key prefix.
	HotState hotstate.HotState

	// TopicPrefix is the unique prefix applied to all topics (e.g., "test-01J5X7...")
	TopicPrefix string

	// KeyPrefix is the unique prefix applied to all Redis keys (e.g., "test:01J5X7...:")
	KeyPrefix string
}

// IsolatedEnvOption configures IsolatedEnv creation.
type IsolatedEnvOption func(*isolatedEnvConfig)

type isolatedEnvConfig struct {
	topicPrefix      string // Override auto-generated topic prefix
	keyPrefix        string // Override auto-generated Redis key prefix
	createTopics     bool   // Auto-create topics (default: true)
	circuitBreaker   *config.CircuitBreakerConfig
	scheduler        *config.SchedulerConfig
	kafkaBrokers     []string
	redisAddr        string
}

// WithTopicPrefix sets a custom topic prefix (for debugging/CI).
func WithTopicPrefix(prefix string) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.topicPrefix = prefix
	}
}

// WithKeyPrefix sets a custom Redis key prefix.
func WithKeyPrefix(prefix string) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.keyPrefix = prefix
	}
}

// WithCircuitBreakerConfig sets custom circuit breaker settings.
func WithCircuitBreakerConfig(cbCfg config.CircuitBreakerConfig) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.circuitBreaker = &cbCfg
	}
}

// WithSchedulerConfig sets custom scheduler settings.
func WithSchedulerConfig(schedCfg config.SchedulerConfig) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.scheduler = &schedCfg
	}
}

// WithoutTopicCreation disables auto topic creation.
func WithoutTopicCreation() IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.createTopics = false
	}
}

// WithKafkaBrokers sets custom Kafka broker addresses.
func WithKafkaBrokers(brokers []string) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.kafkaBrokers = brokers
	}
}

// WithRedisAddr sets custom Redis address.
func WithRedisAddr(addr string) IsolatedEnvOption {
	return func(cfg *isolatedEnvConfig) {
		cfg.redisAddr = addr
	}
}

// NewIsolatedEnv creates an isolated test environment with unique Kafka topics
// and Redis key prefixes. All resources are automatically cleaned up when the
// test completes.
//
// Usage:
//
//	func TestMyFeature(t *testing.T) {
//	    env := testutil.NewIsolatedEnv(t)
//	    // env.Config has isolated topics: test-{ULID}-howk.pending, etc.
//	    // env.HotState uses isolated keys: test:{ULID}:status:*, etc.
//
//	    worker := worker.NewWorker(env.Config, env.Broker, ...)
//	    // ...
//	}
func NewIsolatedEnv(t *testing.T, opts ...IsolatedEnvOption) *IsolatedEnv {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Build internal config from options
	internalCfg := &isolatedEnvConfig{
		createTopics: true,
	}
	for _, opt := range opts {
		opt(internalCfg)
	}

	// Generate or use provided prefixes
	topicPrefix := internalCfg.topicPrefix
	if topicPrefix == "" {
		// Check environment variable override
		if envPrefix := os.Getenv("TEST_TOPIC_PREFIX"); envPrefix != "" {
			topicPrefix = envPrefix
		} else {
			topicPrefix = fmt.Sprintf("test-%s-", ulid.Make().String())
		}
	}

	keyPrefix := internalCfg.keyPrefix
	if keyPrefix == "" {
		// Check environment variable override
		if envPrefix := os.Getenv("TEST_KEY_PREFIX"); envPrefix != "" {
			keyPrefix = envPrefix
		} else {
			keyPrefix = fmt.Sprintf("test:%s:", ulid.Make().String())
		}
	}

	// Get Kafka brokers
	brokers := internalCfg.kafkaBrokers
	if len(brokers) == 0 {
		brokers = getKafkaBrokers()
	}

	// Get Redis address
	redisAddr := internalCfg.redisAddr
	if redisAddr == "" {
		redisAddr = getRedisAddr()
	}

	// Build config with isolated topics
	cfg := config.DefaultConfig()
	cfg.Kafka.Brokers = brokers

	// Apply topic prefix to all topics
	namer := TopicNamer{Prefix: topicPrefix}
	namer.ApplyToConfig(&cfg.Kafka.Topics)

	// Set unique consumer group: test-{ULID}-{testName}
	// Sanitize test name for use in consumer group
	safeTestName := sanitizeConsumerGroupName(t.Name())
	cfg.Kafka.ConsumerGroup = fmt.Sprintf("test-%s-%s", ulid.Make().String(), safeTestName)

	// Aggressive consumer group timing for tests to reduce flakiness
	cfg.Kafka.GroupSessionTimeout = 6 * time.Second
	cfg.Kafka.GroupHeartbeatInterval = 1 * time.Second
	cfg.Kafka.GroupRebalanceTimeout = 6 * time.Second
	// Reduce producer linger for faster test publishing
	cfg.Kafka.ProducerLingerMs = 1

	// Apply custom circuit breaker config if provided
	if internalCfg.circuitBreaker != nil {
		cfg.CircuitBreaker = *internalCfg.circuitBreaker
	}

	// Apply custom scheduler config if provided
	if internalCfg.scheduler != nil {
		cfg.Scheduler = *internalCfg.scheduler
	}

	// Create Kafka broker
	b, err := broker.NewKafkaBroker(cfg.Kafka)
	require.NoError(t, err, "failed to create Kafka broker")

	t.Cleanup(func() {
		b.Close()
	})

	// Create topics if enabled
	if internalCfg.createTopics {
		createIsolatedTopics(t, brokers, topicPrefix)
	}

	// Create Redis hot state with key prefix
	redisCfg := config.RedisConfig{
		Addr: redisAddr,
		DB:   15, // Use test DB
	}

	// Create underlying RedisHotState
	cbConfig := cfg.CircuitBreaker
	ttlConfig := cfg.TTL

	innerHotState, err := hotstate.NewRedisHotState(redisCfg, cbConfig, ttlConfig)
	require.NoError(t, err, "failed to create Redis hot state")

	// Wrap with prefix
	prefixedHotState := NewPrefixedHotState(innerHotState, keyPrefix)

	t.Cleanup(func() {
		// Clean up prefixed keys on cleanup
		ctx := context.Background()
		prefixedHotState.FlushForRebuild(ctx)
		innerHotState.Close()
	})

	return &IsolatedEnv{
		Config:      cfg,
		Broker:      b,
		HotState:    prefixedHotState,
		TopicPrefix: topicPrefix,
		KeyPrefix:   keyPrefix,
	}
}

// TopicNamer generates isolated topic names.
type TopicNamer struct {
	Prefix string // e.g., "test-01J5X7ABCD-"
}

// Apply returns topic name with prefix applied.
// Examples:
//
//	"howk.pending"    -> "test-01J5X7ABCD-howk.pending"
//	"howk.results"    -> "test-01J5X7ABCD-howk.results"
func (n TopicNamer) Apply(baseTopic string) string {
	return n.Prefix + baseTopic
}

// ApplyToConfig modifies a TopicsConfig in-place.
func (n TopicNamer) ApplyToConfig(cfg *config.TopicsConfig) {
	cfg.Pending = n.Apply(cfg.Pending)
	cfg.Results = n.Apply(cfg.Results)
	cfg.DeadLetter = n.Apply(cfg.DeadLetter)
	cfg.Scripts = n.Apply(cfg.Scripts)
	cfg.Slow = n.Apply(cfg.Slow)
}

// createIsolatedTopics creates all 5 topics with the given prefix in parallel.
func createIsolatedTopics(t *testing.T, brokers []string, prefix string) {
	defaultCfg := config.DefaultConfig()
	namer := TopicNamer{Prefix: prefix}

	topics := []string{
		namer.Apply(defaultCfg.Kafka.Topics.Pending),
		namer.Apply(defaultCfg.Kafka.Topics.Results),
		namer.Apply(defaultCfg.Kafka.Topics.DeadLetter),
		namer.Apply(defaultCfg.Kafka.Topics.Scripts),
		namer.Apply(defaultCfg.Kafka.Topics.Slow),
	}

	// Configure Sarama for Redpanda compatibility
	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V3_0_0_0

	admin, err := sarama.NewClusterAdmin(brokers, saramaConfig)
	require.NoError(t, err, "failed to create Kafka admin")
	defer admin.Close()

	// Create topics in parallel for speed
	var wg sync.WaitGroup
	errChan := make(chan error, len(topics))

	for _, topic := range topics {
		wg.Add(1)
		go func(tName string) {
			defer wg.Done()
			err := admin.CreateTopic(tName, &sarama.TopicDetail{
				NumPartitions:     1,
				ReplicationFactor: 1,
			}, false)
			if err != nil {
				errChan <- fmt.Errorf("create topic %s: %w", tName, err)
			}
		}(topic)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		// Ignore "already exists" errors - another parallel test may have created it
		if !strings.Contains(err.Error(), "already exists") {
			t.Logf("Warning: topic creation error (may be benign): %v", err)
		}
	}

	// Register cleanup to delete topics
	t.Cleanup(func() {
		admin, err := sarama.NewClusterAdmin(brokers, saramaConfig)
		if err != nil {
			t.Logf("Warning: failed to create admin for topic cleanup: %v", err)
			return
		}
		defer admin.Close()

		for _, topic := range topics {
			// Ignore errors during cleanup
			_ = admin.DeleteTopic(topic)
		}
	})

	// Wait for topics to be created
	time.Sleep(500 * time.Millisecond)
}

// sanitizeConsumerGroupName sanitizes test name for use in Kafka consumer group.
// Kafka consumer group names can contain: letters, digits, hyphens, underscores, dots
func sanitizeConsumerGroupName(name string) string {
	// Replace invalid characters with underscores
	result := strings.Builder{}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			result.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			result.WriteRune(r)
		case r >= '0' && r <= '9':
			result.WriteRune(r)
		case r == '-', r == '_', r == '.':
			result.WriteRune(r)
		case r == '/':
			// Replace test path separator with underscore
			result.WriteRune('_')
		default:
			result.WriteRune('_')
		}
	}
	return result.String()
}

// getRedisAddr returns Redis address from TEST_REDIS_ADDR env var or defaults
func getRedisAddr() string {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		return "localhost:6380" // Default for docker-compose exposed port
	}
	return addr
}
