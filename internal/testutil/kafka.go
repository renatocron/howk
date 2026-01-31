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

	b, err := broker.NewKafkaBroker(cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		b.Close()
	})

	return b
}

// CreateTestTopic creates a unique topic for a test
func CreateTestTopic(t *testing.T, b *broker.KafkaBroker) string {
	topicName := "test-topic-" + ulid.Make().String()
	brokers := getKafkaBrokers()

	admin, err := sarama.NewClusterAdmin(brokers, nil)
	require.NoError(t, err)
	defer admin.Close()

	err = admin.CreateTopic(topicName, &sarama.TopicDetail{
		NumPartitions:     1,
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
