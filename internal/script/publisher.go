package script

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
)

// Publisher publishes script configurations to Kafka
type Publisher struct {
	broker broker.Broker
	topic  string
}

// NewPublisher creates a new script publisher
func NewPublisher(brk broker.Broker, topic string) *Publisher {
	return &Publisher{
		broker: brk,
		topic:  topic,
	}
}

// PublishScript publishes a script configuration to Kafka
// The script will be published with config_id as the key for compaction
func (p *Publisher) PublishScript(ctx context.Context, script *ScriptConfig) error {
	// Marshal script to JSON
	data, err := json.Marshal(script)
	if err != nil {
		return fmt.Errorf("marshal script config: %w", err)
	}

	// Create Kafka message with config_id as key (for compaction)
	msg := broker.Message{
		Key:   []byte(script.ConfigID),
		Value: data,
		Headers: map[string]string{
			"script_hash": script.Hash,
			"version":     script.Version,
		},
	}

	// Publish to scripts topic
	if err := p.broker.Publish(ctx, p.topic, msg); err != nil {
		return fmt.Errorf("publish script to kafka: %w", err)
	}

	return nil
}

// DeleteScript publishes a tombstone to delete a script from the compacted topic
// This is how Kafka compaction works - a message with null value deletes the key
func (p *Publisher) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	// Create tombstone message (null value)
	msg := broker.Message{
		Key:   []byte(configID),
		Value: nil, // Tombstone - will delete the key from compacted topic
		Headers: map[string]string{
			"operation": "delete",
		},
	}

	// Publish tombstone
	if err := p.broker.Publish(ctx, p.topic, msg); err != nil {
		return fmt.Errorf("publish script tombstone to kafka: %w", err)
	}

	return nil
}
