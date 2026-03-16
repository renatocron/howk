//go:build !integration

package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// --- mockSyncProducer ---

// mockSyncProducer implements sarama.SyncProducer and captures all SendMessages calls
// for assertion in unit tests. Thread-safe via mutex.
type mockSyncProducer struct {
	mu       sync.Mutex
	messages []*sarama.ProducerMessage
	sendErr  error // if non-nil, returned by SendMessages
}

func (m *mockSyncProducer) SendMessage(msg *sarama.ProducerMessage) (int32, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return 0, 0, m.sendErr
	}
	m.messages = append(m.messages, msg)
	return 0, int64(len(m.messages) - 1), nil
}

func (m *mockSyncProducer) SendMessages(msgs []*sarama.ProducerMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sendErr != nil {
		return m.sendErr
	}
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *mockSyncProducer) Close() error                                    { return nil }
func (m *mockSyncProducer) TxnStatus() sarama.ProducerTxnStatusFlag        { return 0 }
func (m *mockSyncProducer) IsTransactional() bool                           { return false }
func (m *mockSyncProducer) BeginTxn() error                                 { return nil }
func (m *mockSyncProducer) CommitTxn() error                                { return nil }
func (m *mockSyncProducer) AbortTxn() error                                 { return nil }
func (m *mockSyncProducer) AddOffsetsToTxn(offsets map[string][]*sarama.PartitionOffsetMetadata, groupId string) error {
	return nil
}
func (m *mockSyncProducer) AddMessageToTxn(msg *sarama.ConsumerMessage, groupId string, metadata *string) error {
	return nil
}

// capturedMessages returns all messages captured so far (copy-safe).
func (m *mockSyncProducer) capturedMessages() []*sarama.ProducerMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*sarama.ProducerMessage, len(m.messages))
	copy(out, m.messages)
	return out
}

// headersMap decodes a sarama message's headers into a plain map for assertion.
func headersMap(msg *sarama.ProducerMessage) map[string]string {
	out := make(map[string]string, len(msg.Headers))
	for _, h := range msg.Headers {
		out[string(h.Key)] = string(h.Value)
	}
	return out
}

// newTestPublisher creates a KafkaWebhookPublisher wired to a mock producer.
func newTestPublisher(mock *mockSyncProducer) (*KafkaWebhookPublisher, *KafkaBroker) {
	topics := config.TopicsConfig{
		Pending:    "howk.pending",
		Results:    "howk.results",
		DeadLetter: "howk.deadletter",
		Scripts:    "howk.scripts",
		Slow:       "howk.slow",
		State:      "howk.state",
	}
	broker := NewKafkaBrokerForTest(mock, nil, config.KafkaConfig{})
	pub := NewKafkaWebhookPublisher(broker, topics)
	return pub, broker
}

// --- KafkaWebhookPublisher tests ---

// TestPublisher_PublishWebhook_TopicAndKey verifies that PublishWebhook routes to the
// pending topic and uses ConfigID as the partition key.
func TestPublisher_PublishWebhook_TopicAndKey(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:           "wh-001",
		ConfigID:     "cfg-abc",
		EndpointHash: "hash-xyz",
		Endpoint:     "https://example.com/hook",
		Payload:      json.RawMessage(`{"event":"click"}`),
		Attempt:      1,
	}

	err := pub.PublishWebhook(context.Background(), webhook)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.pending", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "cfg-abc", string(key))
}

// TestPublisher_PublishWebhook_Headers verifies that PublishWebhook sets the expected
// Kafka headers: config_id, endpoint_hash, attempt.
func TestPublisher_PublishWebhook_Headers(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:           "wh-002",
		ConfigID:     "cfg-headers",
		EndpointHash: "hash-headers",
		Endpoint:     "https://example.com/hook",
		Payload:      json.RawMessage(`{}`),
		Attempt:      3,
	}

	err := pub.PublishWebhook(context.Background(), webhook)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	h := headersMap(msgs[0])
	assert.Equal(t, "cfg-headers", h["config_id"])
	assert.Equal(t, "hash-headers", h["endpoint_hash"])
	assert.Equal(t, "3", h["attempt"])
}

// TestPublisher_PublishWebhook_JSONSerialization verifies that the webhook payload
// is JSON-serialised faithfully in the message value.
func TestPublisher_PublishWebhook_JSONSerialization(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:       "wh-003",
		ConfigID: "cfg-serial",
		Payload:  json.RawMessage(`{"x":42}`),
	}

	err := pub.PublishWebhook(context.Background(), webhook)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	raw, _ := msgs[0].Value.Encode()

	var decoded domain.Webhook
	err = json.Unmarshal(raw, &decoded)
	require.NoError(t, err)
	assert.Equal(t, domain.WebhookID("wh-003"), decoded.ID)
	assert.Equal(t, domain.ConfigID("cfg-serial"), decoded.ConfigID)
}

// TestPublisher_PublishResult_TopicAndKey verifies that PublishResult routes to the
// results topic and uses WebhookID as the partition key.
func TestPublisher_PublishResult_TopicAndKey(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	result := &domain.DeliveryResult{
		WebhookID:    "wh-result-001",
		ConfigID:     "cfg-result",
		EndpointHash: "hash-result",
		Success:      true,
		StatusCode:   200,
	}

	err := pub.PublishResult(context.Background(), result)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.results", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "wh-result-001", string(key))

	h := headersMap(msg)
	assert.Equal(t, "cfg-result", h["config_id"])
	assert.Equal(t, "hash-result", h["endpoint_hash"])
	assert.Equal(t, "true", h["success"])
}

// TestPublisher_PublishDeadLetter_TopicAndKey verifies that PublishDeadLetter routes to
// the deadletter topic and uses WebhookID as the partition key.
func TestPublisher_PublishDeadLetter_TopicAndKey(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:       "wh-dlq-001",
		ConfigID: "cfg-dlq",
	}
	dl := &domain.DeadLetter{
		Webhook:    webhook,
		Reason:     "max retries exceeded",
		ReasonType: domain.DLQReasonExhausted,
	}

	err := pub.PublishDeadLetter(context.Background(), dl)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.deadletter", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "wh-dlq-001", string(key))

	h := headersMap(msg)
	assert.Equal(t, "cfg-dlq", h["config_id"])
	assert.Equal(t, "max retries exceeded", h["reason"])
	assert.Equal(t, "exhausted", h["reason_type"])
}

// TestPublisher_PublishToSlow_TopicKeyAndSourceHeader verifies that PublishToSlow routes
// to the slow topic, uses ConfigID as key, and includes the source=penalty_box header.
func TestPublisher_PublishToSlow_TopicKeyAndSourceHeader(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:           "wh-slow-001",
		ConfigID:     "cfg-slow",
		EndpointHash: "hash-slow",
		Attempt:      5,
		Payload:      json.RawMessage(`{}`),
	}

	err := pub.PublishToSlow(context.Background(), webhook)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.slow", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "cfg-slow", string(key))

	h := headersMap(msg)
	assert.Equal(t, "penalty_box", h["source"])
	assert.Equal(t, "5", h["attempt"])
}

// TestPublisher_PublishState_TopicAndHeaders verifies that PublishState routes to the
// state topic with correct key and headers.
func TestPublisher_PublishState_TopicAndHeaders(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	snapshot := &domain.WebhookStateSnapshot{
		WebhookID: "wh-state-001",
		ConfigID:  "cfg-state",
		State:     domain.StatePending,
	}

	err := pub.PublishState(context.Background(), snapshot)
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.state", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "wh-state-001", string(key))

	h := headersMap(msg)
	assert.Equal(t, "cfg-state", h["config_id"])
	assert.Equal(t, string(domain.StatePending), h["state"])
	assert.Equal(t, "state", h["type"])
}

// TestPublisher_PublishStateTombstone_NilValue verifies that PublishStateTombstone
// sends a nil-value message to the state topic (Kafka log compaction tombstone).
func TestPublisher_PublishStateTombstone_NilValue(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	err := pub.PublishStateTombstone(context.Background(), "wh-tombstone-001")
	require.NoError(t, err)

	msgs := mock.capturedMessages()
	require.Len(t, msgs, 1)

	msg := msgs[0]
	assert.Equal(t, "howk.state", msg.Topic)

	key, _ := msg.Key.Encode()
	assert.Equal(t, "wh-tombstone-001", string(key))

	// Tombstone must have nil value — this triggers log compaction deletion.
	assert.Nil(t, msg.Value)

	h := headersMap(msg)
	assert.Equal(t, "tombstone", h["type"])
}

// TestPublisher_PublishState_NilSnapshot verifies that PublishState returns an error
// when passed a nil snapshot (nil is not a valid state update).
func TestPublisher_PublishState_NilSnapshot(t *testing.T) {
	mock := &mockSyncProducer{}
	pub, _ := newTestPublisher(mock)

	err := pub.PublishState(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "snapshot cannot be nil")

	// No messages should have been sent.
	assert.Empty(t, mock.capturedMessages())
}

// TestPublisher_SendError_Propagated verifies that a producer-level error propagates
// to the caller through the publisher methods.
func TestPublisher_SendError_Propagated(t *testing.T) {
	mock := &mockSyncProducer{sendErr: fmt.Errorf("kafka: broker not available")}
	pub, _ := newTestPublisher(mock)

	webhook := &domain.Webhook{
		ID:      "wh-err",
		Payload: json.RawMessage(`{}`),
	}

	err := pub.PublishWebhook(context.Background(), webhook)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker not available")
}

// --- convertSaramaMessage tests (Step 1.4) ---

// TestConvertSaramaMessage_HeaderMapping verifies that all sarama headers are
// correctly mapped into the broker Message's Headers map.
func TestConvertSaramaMessage_HeaderMapping(t *testing.T) {
	msg := &sarama.ConsumerMessage{
		Key:   []byte("my-key"),
		Value: []byte(`{"id":"123"}`),
		Headers: []*sarama.RecordHeader{
			{Key: []byte("config_id"), Value: []byte("cfg-123")},
			{Key: []byte("attempt"), Value: []byte("2")},
		},
	}

	out := convertSaramaMessage(msg)

	assert.Equal(t, []byte("my-key"), out.Key)
	assert.Equal(t, []byte(`{"id":"123"}`), out.Value)
	assert.Equal(t, "cfg-123", out.Headers["config_id"])
	assert.Equal(t, "2", out.Headers["attempt"])
}

// TestConvertSaramaMessage_EmptyHeaders verifies conversion succeeds with no headers.
func TestConvertSaramaMessage_EmptyHeaders(t *testing.T) {
	msg := &sarama.ConsumerMessage{
		Key:     []byte("key"),
		Value:   []byte("value"),
		Headers: nil,
	}

	out := convertSaramaMessage(msg)

	assert.Equal(t, []byte("key"), out.Key)
	assert.Equal(t, []byte("value"), out.Value)
	assert.NotNil(t, out.Headers)
	assert.Empty(t, out.Headers)
}

// TestConvertSaramaMessage_NilKeyValue verifies conversion handles nil key and value.
func TestConvertSaramaMessage_NilKeyValue(t *testing.T) {
	msg := &sarama.ConsumerMessage{
		Key:   nil,
		Value: nil,
	}

	out := convertSaramaMessage(msg)

	assert.Nil(t, out.Key)
	assert.Nil(t, out.Value)
	assert.Empty(t, out.Headers)
}

// TestConvertSaramaMessage_DuplicateHeaderLastWins verifies that if the same header
// key appears twice, the last value wins (map semantics).
func TestConvertSaramaMessage_DuplicateHeaderLastWins(t *testing.T) {
	msg := &sarama.ConsumerMessage{
		Key:   []byte("k"),
		Value: []byte("v"),
		Headers: []*sarama.RecordHeader{
			{Key: []byte("x"), Value: []byte("first")},
			{Key: []byte("x"), Value: []byte("second")},
		},
	}

	out := convertSaramaMessage(msg)

	assert.Equal(t, "second", out.Headers["x"])
}

// --- getOrCreateKeyChannel concurrency tests (Step 1.5) ---

// newPartitionProcessorForTest creates a minimal partitionProcessor suitable for
// testing getOrCreateKeyChannel without real Kafka dependencies.
func newPartitionProcessorForTest() *partitionProcessor {
	ctx, cancel := context.WithCancel(context.Background())
	return &partitionProcessor{
		keyChannels:       make(map[string]chan *keyedMessage),
		ctx:               ctx,
		cancel:            cancel,
		perKeyParallelism: 1,
		handler: func(ctx context.Context, msg *Message) error {
			return nil
		},
	}
}

// TestGetOrCreateKeyChannel_SameKeyReturnsSameChannel verifies that concurrent
// callers requesting the same key always get the identical channel instance.
func TestGetOrCreateKeyChannel_SameKeyReturnsSameChannel(t *testing.T) {
	pp := newPartitionProcessorForTest()
	defer pp.cancel()

	const goroutines = 20
	channels := make([]chan *keyedMessage, goroutines)
	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			channels[i] = pp.getOrCreateKeyChannel("same-key")
		}()
	}
	wg.Wait()

	first := channels[0]
	for i := 1; i < goroutines; i++ {
		assert.Equal(t, first, channels[i], "goroutine %d got a different channel", i)
	}
}

// TestGetOrCreateKeyChannel_DifferentKeysGetDifferentChannels verifies that different
// keys produce distinct, independent channels.
func TestGetOrCreateKeyChannel_DifferentKeysGetDifferentChannels(t *testing.T) {
	pp := newPartitionProcessorForTest()
	defer pp.cancel()

	ch1 := pp.getOrCreateKeyChannel("key-alpha")
	ch2 := pp.getOrCreateKeyChannel("key-beta")
	ch3 := pp.getOrCreateKeyChannel("key-gamma")

	assert.NotEqual(t, ch1, ch2)
	assert.NotEqual(t, ch2, ch3)
	assert.NotEqual(t, ch1, ch3)
}

// TestGetOrCreateKeyChannel_IdempotentForSameKey verifies that sequential calls for
// the same key return the same channel (deterministic, not just concurrent-safe).
func TestGetOrCreateKeyChannel_IdempotentForSameKey(t *testing.T) {
	pp := newPartitionProcessorForTest()
	defer pp.cancel()

	ch1 := pp.getOrCreateKeyChannel("idempotent-key")
	ch2 := pp.getOrCreateKeyChannel("idempotent-key")
	ch3 := pp.getOrCreateKeyChannel("idempotent-key")

	assert.Equal(t, ch1, ch2)
	assert.Equal(t, ch2, ch3)
}

// TestGetOrCreateKeyChannel_ConcurrentDistinctKeys verifies that concurrent creation
// of distinct keys does not produce races and each key maps to a unique channel.
func TestGetOrCreateKeyChannel_ConcurrentDistinctKeys(t *testing.T) {
	pp := newPartitionProcessorForTest()
	defer pp.cancel()

	const n = 30
	channels := make([]chan *keyedMessage, n)
	var wg sync.WaitGroup

	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i)
			channels[i] = pp.getOrCreateKeyChannel(key)
		}()
	}
	wg.Wait()

	// Verify all n channels are distinct (no two keys share a channel).
	// Use a sync.Map as an identity set, keyed by the channel's fmt pointer string.
	var seen sync.Map
	for i, ch := range channels {
		ptr := fmt.Sprintf("%p", ch)
		_, alreadySeen := seen.LoadOrStore(ptr, true)
		assert.False(t, alreadySeen, "key-%d shares a channel pointer with another key", i)
	}
}
