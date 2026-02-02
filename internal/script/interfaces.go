package script

import (
	"context"

	"github.com/howk/howk/internal/domain"
)

// ValidatorInterface defines the contract for Lua script validation
type ValidatorInterface interface {
	// ValidateSyntax checks if the Lua code is syntactically valid
	// Returns nil if valid, error if syntax error detected
	ValidateSyntax(luaCode string) error
}

// PublisherInterface defines the contract for publishing scripts to Kafka
type PublisherInterface interface {
	// PublishScript publishes a script configuration to Kafka
	// The script will be published with config_id as the key for compaction
	PublishScript(ctx context.Context, script *ScriptConfig) error

	// DeleteScript publishes a tombstone to delete a script from the compacted topic
	// This is how Kafka compaction works - a message with null value deletes the key
	DeleteScript(ctx context.Context, configID domain.ConfigID) error
}

// Compile-time assertions to ensure concrete types implement interfaces
var _ ValidatorInterface = (*Validator)(nil)
var _ PublisherInterface = (*Publisher)(nil)
