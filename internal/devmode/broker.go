package devmode

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

const channelBuffer = 1000

// MemBroker is an in-memory message broker for dev mode.
// It replaces Kafka with channel-based pub/sub.
type MemBroker struct {
	mu     sync.RWMutex
	subs   map[string][]chan *broker.Message
	closed bool
}

var _ broker.Broker = (*MemBroker)(nil)

func NewMemBroker() *MemBroker {
	return &MemBroker{
		subs: make(map[string][]chan *broker.Message),
	}
}

func (b *MemBroker) Publish(ctx context.Context, topic string, msgs ...broker.Message) error {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return fmt.Errorf("broker closed")
	}

	channels := b.subs[topic]
	for i := range msgs {
		msg := msgs[i] // copy
		for _, ch := range channels {
			select {
			case ch <- &msg:
			default:
				log.Warn().Str("topic", topic).Msg("devmode: subscriber channel full, dropping message")
			}
		}
	}
	return nil
}

func (b *MemBroker) Subscribe(ctx context.Context, topic, group string, handler broker.Handler) error {
	ch := make(chan *broker.Message, channelBuffer)

	b.mu.Lock()
	b.subs[topic] = append(b.subs[topic], ch)
	b.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			if err := handler(ctx, msg); err != nil {
				log.Warn().Err(err).Str("topic", topic).Msg("devmode: handler error")
			}
		}
	}
}

func (b *MemBroker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}
	b.closed = true
	for _, channels := range b.subs {
		for _, ch := range channels {
			close(ch)
		}
	}
	return nil
}

// MemWebhookPublisher implements broker.WebhookPublisher using a MemBroker.
type MemWebhookPublisher struct {
	broker broker.Broker
	topics config.TopicsConfig
}

var _ broker.WebhookPublisher = (*MemWebhookPublisher)(nil)

func NewMemWebhookPublisher(brk broker.Broker, topics config.TopicsConfig) *MemWebhookPublisher {
	return &MemWebhookPublisher{broker: brk, topics: topics}
}

func (p *MemWebhookPublisher) publishGeneric(ctx context.Context, topic, key string, payload any, headers map[string]string) error {
	var value []byte
	var err error

	if payload != nil {
		value, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
	}

	return p.broker.Publish(ctx, topic, broker.Message{
		Key:     []byte(key),
		Value:   value,
		Headers: headers,
	})
}

func (p *MemWebhookPublisher) PublishWebhook(ctx context.Context, webhook *domain.Webhook) error {
	return p.publishGeneric(ctx, p.topics.Pending, string(webhook.ConfigID), webhook, map[string]string{
		"config_id":     string(webhook.ConfigID),
		"endpoint_hash": string(webhook.EndpointHash),
		"attempt":       fmt.Sprintf("%d", webhook.Attempt),
	})
}

func (p *MemWebhookPublisher) PublishResult(ctx context.Context, result *domain.DeliveryResult) error {
	return p.publishGeneric(ctx, p.topics.Results, string(result.WebhookID), result, map[string]string{
		"config_id":     string(result.ConfigID),
		"endpoint_hash": string(result.EndpointHash),
		"success":       fmt.Sprintf("%t", result.Success),
	})
}

func (p *MemWebhookPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	return p.publishGeneric(ctx, p.topics.DeadLetter, string(dl.Webhook.ID), dl, map[string]string{
		"config_id":   string(dl.Webhook.ConfigID),
		"reason":      dl.Reason,
		"reason_type": string(dl.ReasonType),
	})
}

func (p *MemWebhookPublisher) PublishToSlow(ctx context.Context, webhook *domain.Webhook) error {
	return p.publishGeneric(ctx, p.topics.Slow, string(webhook.ConfigID), webhook, map[string]string{
		"config_id":     string(webhook.ConfigID),
		"endpoint_hash": string(webhook.EndpointHash),
		"attempt":       fmt.Sprintf("%d", webhook.Attempt),
		"source":        "penalty_box",
	})
}

func (p *MemWebhookPublisher) PublishState(ctx context.Context, snapshot *domain.WebhookStateSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("snapshot cannot be nil, use PublishStateTombstone for deletion")
	}
	return p.publishGeneric(ctx, p.topics.State, string(snapshot.WebhookID), snapshot, map[string]string{
		"config_id": string(snapshot.ConfigID),
		"state":     string(snapshot.State),
		"type":      "state",
	})
}

func (p *MemWebhookPublisher) PublishStateTombstone(ctx context.Context, webhookID domain.WebhookID) error {
	return p.publishGeneric(ctx, p.topics.State, string(webhookID), nil, map[string]string{
		"type": "tombstone",
	})
}

func (p *MemWebhookPublisher) Close() error {
	return p.broker.Close()
}
