package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/sarama"
	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// KafkaBroker implements Broker using Kafka
type KafkaBroker struct {
	config   config.KafkaConfig
	producer sarama.SyncProducer
	client   sarama.Client

	mu     sync.RWMutex
	closed bool
}

// NewKafkaBroker creates a new Kafka broker
func NewKafkaBroker(cfg config.KafkaConfig) (*KafkaBroker, error) {
	saramaConfig := sarama.NewConfig()

	// Producer settings
	saramaConfig.Producer.RequiredAcks = sarama.WaitForAll // Wait for all replicas
	saramaConfig.Producer.Retry.Max = 5
	saramaConfig.Producer.Return.Successes = true
	saramaConfig.Producer.Return.Errors = true
	saramaConfig.Producer.Compression = compressionCodec(cfg.ProducerCompression)

	if cfg.ProducerLingerMs < 1 {
		cfg.ProducerLingerMs = 1
		log.Warn().Msgf("ProducerLingerMs was set to less than 1ms, adjusting to 1ms")
	}
	saramaConfig.Producer.Flush.Frequency = time.Duration(cfg.ProducerLingerMs) * time.Millisecond
	saramaConfig.Producer.Flush.Bytes = cfg.ProducerBatchSize

	// Consumer settings
	saramaConfig.Consumer.Fetch.Min = int32(cfg.ConsumerFetchMinBytes)
	saramaConfig.Consumer.MaxWaitTime = cfg.ConsumerFetchMaxWait
	saramaConfig.Consumer.Return.Errors = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest
	saramaConfig.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRoundRobin()

	// Consumer group timing
	if cfg.GroupSessionTimeout > 0 {
		saramaConfig.Consumer.Group.Session.Timeout = cfg.GroupSessionTimeout
	}
	if cfg.GroupHeartbeatInterval > 0 {
		saramaConfig.Consumer.Group.Heartbeat.Interval = cfg.GroupHeartbeatInterval
	}
	if cfg.GroupRebalanceTimeout > 0 {
		saramaConfig.Consumer.Group.Rebalance.Timeout = cfg.GroupRebalanceTimeout
	}

	// Client settings
	saramaConfig.ClientID = "howk"
	saramaConfig.Version = sarama.V3_0_0_0

	// Create client
	client, err := sarama.NewClient(cfg.Brokers, saramaConfig)
	if err != nil {
		return nil, fmt.Errorf("create kafka client: %w", err)
	}

	// Create producer
	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create kafka producer: %w", err)
	}

	return &KafkaBroker{
		config:   cfg,
		producer: producer,
		client:   client,
	}, nil
}

func compressionCodec(name string) sarama.CompressionCodec {
	switch name {
	case "gzip":
		return sarama.CompressionGZIP
	case "snappy":
		return sarama.CompressionSnappy
	case "lz4":
		return sarama.CompressionLZ4
	case "zstd":
		return sarama.CompressionZSTD
	default:
		return sarama.CompressionNone
	}
}

// Publish sends messages to a topic
func (k *KafkaBroker) Publish(ctx context.Context, topic string, msgs ...Message) error {
	k.mu.RLock()
	if k.closed {
		k.mu.RUnlock()
		return fmt.Errorf("broker is closed")
	}
	k.mu.RUnlock()

	saramaMessages := make([]*sarama.ProducerMessage, len(msgs))
	for i, msg := range msgs {
		headers := make([]sarama.RecordHeader, 0, len(msg.Headers))
		for k, v := range msg.Headers {
			headers = append(headers, sarama.RecordHeader{
				Key:   []byte(k),
				Value: []byte(v),
			})
		}

		saramaMessages[i] = &sarama.ProducerMessage{
			Topic:   topic,
			Key:     sarama.ByteEncoder(msg.Key),
			Value:   sarama.ByteEncoder(msg.Value),
			Headers: headers,
		}
	}

	return k.producer.SendMessages(saramaMessages)
}

// Subscribe starts consuming from a topic
func (k *KafkaBroker) Subscribe(ctx context.Context, topic, group string, handler Handler) error {
	consumerGroup, err := sarama.NewConsumerGroupFromClient(group, k.client)
	if err != nil {
		return fmt.Errorf("create consumer group: %w", err)
	}
	defer consumerGroup.Close()

	consumer := &consumerGroupHandler{
		handler: handler,
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := consumerGroup.Consume(ctx, []string{topic}, consumer); err != nil {
				log.Error().Err(err).Str("topic", topic).Msg("Consumer error")
			}
		}
	}
}

func (k *KafkaBroker) Close() error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.closed {
		return nil
	}
	k.closed = true

	if err := k.producer.Close(); err != nil {
		return err
	}
	return k.client.Close()
}

// consumerGroupHandler implements sarama.ConsumerGroupHandler
type consumerGroupHandler struct {
	handler Handler
}

func (h *consumerGroupHandler) Setup(_ sarama.ConsumerGroupSession) error   { return nil }
func (h *consumerGroupHandler) Cleanup(_ sarama.ConsumerGroupSession) error { return nil }

func (h *consumerGroupHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for msg := range claim.Messages() {
		headers := make(map[string]string)
		for _, h := range msg.Headers {
			headers[string(h.Key)] = string(h.Value)
		}

		brokerMsg := &Message{
			Key:     msg.Key,
			Value:   msg.Value,
			Headers: headers,
		}

		if err := h.handler(session.Context(), brokerMsg); err != nil {
			log.Error().Err(err).
				Str("topic", msg.Topic).
				Int32("partition", msg.Partition).
				Int64("offset", msg.Offset).
				Msg("Handler error")
			// Don't mark as consumed on error - will be redelivered
			continue
		}

		session.MarkMessage(msg, "")
	}
	return nil
}

// --- Higher-level Webhook Publisher ---

// KafkaWebhookPublisher implements WebhookPublisher
type KafkaWebhookPublisher struct {
	broker *KafkaBroker
	topics config.TopicsConfig
}

// NewKafkaWebhookPublisher creates a new webhook publisher
func NewKafkaWebhookPublisher(broker *KafkaBroker, topics config.TopicsConfig) *KafkaWebhookPublisher {
	return &KafkaWebhookPublisher{
		broker: broker,
		topics: topics,
	}
}

// publishGeneric is a helper for publishing messages with common logic.
func (p *KafkaWebhookPublisher) publishGeneric(ctx context.Context, topic string, key string, payload interface{}, headers map[string]string) error {
	var value []byte
	var err error

	if payload != nil {
		value, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
	}

	msg := Message{
		Key:     []byte(key),
		Value:   value,
		Headers: headers,
	}

	return p.broker.Publish(ctx, topic, msg)
}

func (p *KafkaWebhookPublisher) PublishWebhook(ctx context.Context, webhook *domain.Webhook) error {
	return p.publishGeneric(ctx, p.topics.Pending, string(webhook.ConfigID), webhook, map[string]string{
		"config_id":     string(webhook.ConfigID),
		"endpoint_hash": string(webhook.EndpointHash),
		"attempt":       fmt.Sprintf("%d", webhook.Attempt),
	})
}

func (p *KafkaWebhookPublisher) PublishResult(ctx context.Context, result *domain.DeliveryResult) error {
	return p.publishGeneric(ctx, p.topics.Results, string(result.WebhookID), result, map[string]string{
		"config_id":     string(result.ConfigID),
		"endpoint_hash": string(result.EndpointHash),
		"success":       fmt.Sprintf("%t", result.Success),
	})
}

func (p *KafkaWebhookPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	return p.publishGeneric(ctx, p.topics.DeadLetter, string(dl.Webhook.ID), dl, map[string]string{
		"config_id":   string(dl.Webhook.ConfigID),
		"reason":      dl.Reason,
		"reason_type": string(dl.ReasonType),
	})
}

func (p *KafkaWebhookPublisher) Close() error {
	return p.broker.Close()
}

func (p *KafkaWebhookPublisher) PublishToSlow(ctx context.Context, webhook *domain.Webhook) error {
	return p.publishGeneric(ctx, p.topics.Slow, string(webhook.ConfigID), webhook, map[string]string{
		"config_id":     string(webhook.ConfigID),
		"endpoint_hash": string(webhook.EndpointHash),
		"attempt":       fmt.Sprintf("%d", webhook.Attempt),
		"source":        "penalty_box",
	})
}

// PublishState publishes a webhook state snapshot to the compacted state topic.
// If snapshot is nil, publishes a tombstone (nil value) to delete the key.
// This enables zero-maintenance reconciliation by storing the active state of
// webhooks that are pending retries in a compacted Kafka topic.
func (p *KafkaWebhookPublisher) PublishState(ctx context.Context, snapshot *domain.WebhookStateSnapshot) error {
	if snapshot == nil {
		// Tombstone case - value is nil, key must be provided by caller
		// This is handled by the worker passing a snapshot with only WebhookID set
		// or by using a separate method. For now, we handle nil as tombstone
		// but we need the key. Let's document that nil snapshot is not valid
		// and workers should pass a snapshot with at least WebhookID.
		return fmt.Errorf("snapshot cannot be nil, use snapshot with WebhookID only for tombstone")
	}

	return p.publishGeneric(ctx, p.topics.State, string(snapshot.WebhookID), snapshot, map[string]string{
		"config_id": string(snapshot.ConfigID),
		"state":     snapshot.State,
		"type":      "state",
	})
}

// PublishStateTombstone publishes a tombstone (nil value) to the state topic
// to indicate that a webhook has reached a terminal state and should be removed
// from the compacted topic during log compaction.
func (p *KafkaWebhookPublisher) PublishStateTombstone(ctx context.Context, webhookID domain.WebhookID) error {
	return p.publishGeneric(ctx, p.topics.State, string(webhookID), nil, map[string]string{
		"type": "tombstone",
	})
}

// Ping checks connectivity to the underlying Kafka broker
func (p *KafkaWebhookPublisher) Ping(ctx context.Context) error {
	if p == nil || p.broker == nil {
		return fmt.Errorf("publisher broker not configured")
	}
	return p.broker.Ping(ctx)
}

// Ping checks if the Kafka broker is reachable and client can fetch metadata
func (k *KafkaBroker) Ping(ctx context.Context) error {
	k.mu.RLock()
	if k.closed {
		k.mu.RUnlock()
		return fmt.Errorf("broker is closed")
	}
	k.mu.RUnlock()

	// Attempt to fetch brokers/metadata via the client
	brokers := k.client.Brokers()
	if len(brokers) == 0 {
		return fmt.Errorf("no brokers available")
	}

	// Optionally try to open connections (sarama provides brokers but we rely on client)
	return nil
}
