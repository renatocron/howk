//go:build integration

package testutil

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// getKafkaBrokers returns Kafka brokers from TEST_KAFKA_BROKERS env var or defaults
func getKafkaBrokers() []string {
	brokers := os.Getenv("TEST_KAFKA_BROKERS")
	if brokers == "" {
		return []string{"localhost:19092"} // Default for docker-compose exposed port
	}
	return strings.Split(brokers, ",")
}

// SetupKafka connects to Kafka (uses TEST_KAFKA_BROKERS env var, defaults to localhost:19092)
func SetupKafka(t *testing.T) *broker.KafkaBroker {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	brokers := getKafkaBrokers()

	cfg := config.DefaultConfig().Kafka
	cfg.Brokers = brokers
	// Aggressive consumer group timing for tests to reduce flakiness
	cfg.GroupSessionTimeout = 6 * time.Second
	cfg.GroupHeartbeatInterval = 1 * time.Second
	cfg.GroupRebalanceTimeout = 6 * time.Second
	// Reduce producer linger for faster test publishing
	cfg.ProducerLingerMs = 1

	b, err := broker.NewKafkaBroker(cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		b.Close()
	})

	return b
}

// CreateTestTopic creates a unique topic for a test with 1 partition
func CreateTestTopic(t *testing.T, b *broker.KafkaBroker) string {
	return CreateTestTopicWithPartitions(t, b, 1)
}

// CreateTestTopicWithPartitions creates a unique topic with the specified number of partitions
func CreateTestTopicWithPartitions(t *testing.T, b *broker.KafkaBroker, numPartitions int32) string {
	topicName := "test-topic-" + ulid.Make().String()
	brokers := getKafkaBrokers()

	// Configure Sarama for Redpanda compatibility
	saramaConfig := sarama.NewConfig()
	saramaConfig.Version = sarama.V3_0_0_0

	admin, err := sarama.NewClusterAdmin(brokers, saramaConfig)
	require.NoError(t, err)
	defer admin.Close()

	err = admin.CreateTopic(topicName, &sarama.TopicDetail{
		NumPartitions:     numPartitions,
		ReplicationFactor: 1,
	}, false)
	require.NoError(t, err)

	t.Cleanup(func() {
		admin.DeleteTopic(topicName)
	})

	// Wait for topic to be created
	time.Sleep(1 * time.Second)

	return topicName
}
