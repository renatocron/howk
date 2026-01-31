package broker

import (
	"context"

	"github.com/howk/howk/internal/domain"
)

// Message represents a message to be published or consumed
type Message struct {
	Key     []byte
	Value   []byte
	Headers map[string]string
}

// Handler processes incoming messages
type Handler func(ctx context.Context, msg *Message) error

// Broker abstracts the message broker (Kafka)
type Broker interface {
	// Publish sends messages to a topic (batched internally)
	Publish(ctx context.Context, topic string, msgs ...Message) error

	// Subscribe starts consuming from a topic with the given handler
	// This is blocking and should be run in a goroutine
	Subscribe(ctx context.Context, topic, group string, handler Handler) error

	// Close gracefully shuts down the broker
	Close() error
}

// WebhookPublisher is a higher-level interface for publishing webhooks
type WebhookPublisher interface {
	// PublishWebhook publishes a webhook to the pending topic
	PublishWebhook(ctx context.Context, webhook *domain.Webhook) error

	// PublishResult publishes a delivery result
	PublishResult(ctx context.Context, result *domain.DeliveryResult) error

	// PublishDeadLetter publishes to the dead letter topic
	PublishDeadLetter(ctx context.Context, webhook *domain.Webhook, reason string) error

	Close() error
}

// WebhookConsumer is a higher-level interface for consuming webhooks
type WebhookConsumer interface {
	// ConsumePending starts consuming from the pending topic
	ConsumePending(ctx context.Context, handler func(context.Context, *domain.Webhook) error) error

	// ConsumeResults starts consuming from the results topic
	ConsumeResults(ctx context.Context, handler func(context.Context, *domain.DeliveryResult) error) error

	Close() error
}
