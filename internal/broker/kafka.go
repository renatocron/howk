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
	saramaConfig.Producer.Flush.Frequency = time.Duration(cfg.ProducerLingerMs) * time.Millisecond
	saramaConfig.Producer.Flush.Bytes = cfg.ProducerBatchSize

	// Consumer settings
	saramaConfig.Consumer.Fetch.Min = int32(cfg.ConsumerFetchMinBytes)
	saramaConfig.Consumer.MaxWaitTime = cfg.ConsumerFetchMaxWait
	saramaConfig.Consumer.Return.Errors = true
	saramaConfig.Consumer.Offsets.Initial = sarama.OffsetNewest
	saramaConfig.Consumer.Group.Rebalance.Strategy = sarama.NewBalanceStrategyRoundRobin()

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

func (p *KafkaWebhookPublisher) PublishWebhook(ctx context.Context, webhook *domain.Webhook) error {
	data, err := json.Marshal(webhook)
	if err != nil {
		return fmt.Errorf("marshal webhook: %w", err)
	}

	msg := Message{
		Key:   []byte(webhook.ID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(webhook.ConfigID),
			"endpoint_hash": string(webhook.EndpointHash),
			"attempt":       fmt.Sprintf("%d", webhook.Attempt),
		},
	}

	return p.broker.Publish(ctx, p.topics.Pending, msg)
}

func (p *KafkaWebhookPublisher) PublishResult(ctx context.Context, result *domain.DeliveryResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	msg := Message{
		Key:   []byte(result.WebhookID),
		Value: data,
		Headers: map[string]string{
			"config_id":     string(result.ConfigID),
			"endpoint_hash": string(result.EndpointHash),
			"success":       fmt.Sprintf("%t", result.Success),
		},
	}

	return p.broker.Publish(ctx, p.topics.Results, msg)
}

func (p *KafkaWebhookPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	data, err := json.Marshal(dl)
	if err != nil {
		return fmt.Errorf("marshal dead letter: %w", err)
	}

	msg := Message{
		Key:   []byte(dl.Webhook.ID),
		Value: data,
		Headers: map[string]string{
			"config_id":   string(dl.Webhook.ConfigID),
			"reason":      dl.Reason,
			"reason_type": string(dl.ReasonType),
		},
	}

	return p.broker.Publish(ctx, p.topics.DeadLetter, msg)
}

func (p *KafkaWebhookPublisher) Close() error {
	return p.broker.Close()
}
