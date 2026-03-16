//go:build !integration

package reconciler

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/howk/howk/internal/config"
	"github.com/howk/howk/internal/domain"
)

// mockSaramaConsumer implements sarama.Consumer for testing.
// Only the methods called by consumePartition are required.
type mockSaramaConsumer struct {
	mock.Mock
}

func (m *mockSaramaConsumer) Topics() ([]string, error) {
	args := m.Called()
	return args.Get(0).([]string), args.Error(1)
}

func (m *mockSaramaConsumer) Partitions(topic string) ([]int32, error) {
	args := m.Called(topic)
	return args.Get(0).([]int32), args.Error(1)
}

func (m *mockSaramaConsumer) ConsumePartition(topic string, partition int32, offset int64) (sarama.PartitionConsumer, error) {
	args := m.Called(topic, partition, offset)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(sarama.PartitionConsumer), args.Error(1)
}

func (m *mockSaramaConsumer) HighWaterMarks() map[string]map[int32]int64 {
	return m.Called().Get(0).(map[string]map[int32]int64)
}

func (m *mockSaramaConsumer) Close() error {
	return m.Called().Error(0)
}

func (m *mockSaramaConsumer) Pause(topicPartitions map[string][]int32) {
	m.Called(topicPartitions)
}

func (m *mockSaramaConsumer) Resume(topicPartitions map[string][]int32) {
	m.Called(topicPartitions)
}

func (m *mockSaramaConsumer) PauseAll() {
	m.Called()
}

func (m *mockSaramaConsumer) ResumeAll() {
	m.Called()
}

// mockPartitionConsumer implements sarama.PartitionConsumer for testing.
// Messages are delivered via a buffered channel that is closed after the
// canned set, mirroring how sarama signals end-of-stream.
type mockPartitionConsumer struct {
	mock.Mock
	messages chan *sarama.ConsumerMessage
	hwm      int64
}

func newMockPartitionConsumer(hwm int64) *mockPartitionConsumer {
	return &mockPartitionConsumer{
		messages: make(chan *sarama.ConsumerMessage, 16),
		hwm:      hwm,
	}
}

func (m *mockPartitionConsumer) AsyncClose() { m.Called() }

func (m *mockPartitionConsumer) Close() error {
	return m.Called().Error(0)
}

func (m *mockPartitionConsumer) Messages() <-chan *sarama.ConsumerMessage {
	return m.messages
}

func (m *mockPartitionConsumer) Errors() <-chan *sarama.ConsumerError {
	return make(chan *sarama.ConsumerError)
}

func (m *mockPartitionConsumer) HighWaterMarkOffset() int64 {
	return m.hwm
}

func (m *mockPartitionConsumer) Pause()    { m.Called() }
func (m *mockPartitionConsumer) Resume()   { m.Called() }
func (m *mockPartitionConsumer) IsPaused() bool {
	return m.Called().Bool(0)
}

// ---- helpers ----------------------------------------------------------------

func newTestReconciler(hs *MockHotState) *Reconciler {
	return &Reconciler{
		config: config.KafkaConfig{
			Topics: config.TopicsConfig{State: "howk.state"},
		},
		hotstate:  hs,
		ttlConfig: config.TTLConfig{RetryDataTTL: time.Hour},
	}
}

func makeSnapshotBytes(t *testing.T, snap domain.WebhookStateSnapshot) []byte {
	t.Helper()
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return b
}

// sendAndClose pushes msgs into the partition consumer and then closes the
// channel so the range loop in consumePartition terminates.
func sendAndClose(pc *mockPartitionConsumer, msgs ...*sarama.ConsumerMessage) {
	for _, m := range msgs {
		pc.messages <- m
	}
	close(pc.messages)
}

// ---- tests ------------------------------------------------------------------

// TestConsumePartition_ValidSnapshot verifies that a valid snapshot restores
// state and increments restoredCount.
func TestConsumePartition_ValidSnapshot(t *testing.T) {
	mockHS := new(MockHotState)
	r := newTestReconciler(mockHS)

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	snap := domain.WebhookStateSnapshot{
		WebhookID:   webhookID,
		State:       domain.StateDelivered,
		Attempt:     1,
		MaxAttempts: 10,
		CreatedAt:   now,
	}

	pc := newMockPartitionConsumer(2) // HWM = 2, so offset 1 is the last
	pc.On("Close").Return(nil)

	msg := &sarama.ConsumerMessage{
		Offset: 1,
		Key:    []byte(string(webhookID)),
		Value:  makeSnapshotBytes(t, snap),
	}
	sendAndClose(pc, msg)

	consumer := new(mockSaramaConsumer)
	consumer.On("ConsumePartition", "howk.state", int32(0), sarama.OffsetOldest).Return(pc, nil)

	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.WebhookID == webhookID && s.State == domain.StateDelivered
	})).Return(nil)

	var restored, tombstones, parseErrors int64
	err := r.consumePartition(context.Background(), consumer, 0, &restored, &tombstones, &parseErrors)

	assert.NoError(t, err)
	assert.Equal(t, int64(1), restored)
	assert.Equal(t, int64(0), tombstones)
	assert.Equal(t, int64(0), parseErrors)

	mockHS.AssertExpectations(t)
	consumer.AssertExpectations(t)
	pc.AssertExpectations(t)
}

// TestConsumePartition_TombstoneSkipped verifies that nil-value messages
// (Kafka tombstones) increment tombstoneCount and are not restored.
func TestConsumePartition_TombstoneSkipped(t *testing.T) {
	mockHS := new(MockHotState)
	r := newTestReconciler(mockHS)

	pc := newMockPartitionConsumer(2)
	pc.On("Close").Return(nil)

	tombstone := &sarama.ConsumerMessage{
		Offset: 1,
		Key:    []byte("wh_tombstone"),
		Value:  nil, // tombstone
	}
	sendAndClose(pc, tombstone)

	consumer := new(mockSaramaConsumer)
	consumer.On("ConsumePartition", "howk.state", int32(0), sarama.OffsetOldest).Return(pc, nil)

	var restored, tombstones, parseErrors int64
	err := r.consumePartition(context.Background(), consumer, 0, &restored, &tombstones, &parseErrors)

	assert.NoError(t, err)
	assert.Equal(t, int64(0), restored)
	assert.Equal(t, int64(1), tombstones)
	assert.Equal(t, int64(0), parseErrors)

	// SetStatus must NOT be called for a tombstone.
	mockHS.AssertNotCalled(t, "SetStatus", mock.Anything, mock.Anything)
	consumer.AssertExpectations(t)
	pc.AssertExpectations(t)
}

// TestConsumePartition_ParseError verifies that a malformed JSON value
// increments parseErrorCount without aborting the loop.
func TestConsumePartition_ParseError(t *testing.T) {
	mockHS := new(MockHotState)
	r := newTestReconciler(mockHS)

	pc := newMockPartitionConsumer(2)
	pc.On("Close").Return(nil)

	bad := &sarama.ConsumerMessage{
		Offset: 1,
		Key:    []byte("wh_bad"),
		Value:  []byte("not-valid-json{{{"),
	}
	sendAndClose(pc, bad)

	consumer := new(mockSaramaConsumer)
	consumer.On("ConsumePartition", "howk.state", int32(0), sarama.OffsetOldest).Return(pc, nil)

	var restored, tombstones, parseErrors int64
	err := r.consumePartition(context.Background(), consumer, 0, &restored, &tombstones, &parseErrors)

	assert.NoError(t, err)
	assert.Equal(t, int64(0), restored)
	assert.Equal(t, int64(0), tombstones)
	assert.Equal(t, int64(1), parseErrors)

	mockHS.AssertNotCalled(t, "SetStatus", mock.Anything, mock.Anything)
	consumer.AssertExpectations(t)
	pc.AssertExpectations(t)
}

// TestConsumePartition_ContextCancellation verifies that cancelling the
// context causes consumePartition to return context.Canceled.
//
// Strategy: cancel the context BEFORE putting a message in the channel.
// consumePartition's inner loop checks ctx.Done() at the top of each
// iteration (after receiving a message). The pre-cancelled context is
// detected on the first message, returning context.Canceled immediately.
func TestConsumePartition_ContextCancellation(t *testing.T) {
	mockHS := new(MockHotState)
	r := newTestReconciler(mockHS)

	// HWM is large so the high-water-mark break never fires.
	pc := newMockPartitionConsumer(100)
	pc.On("Close").Return(nil)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel BEFORE the message is consumed so the ctx.Done() check in
	// the loop body sees it immediately on the first iteration.
	cancel()

	now := time.Now()
	webhookID := domain.WebhookID(ulid.Make().String())
	snap := domain.WebhookStateSnapshot{
		WebhookID:   webhookID,
		State:       domain.StateDelivered,
		Attempt:     1,
		MaxAttempts: 10,
		CreatedAt:   now,
	}

	// A single message triggers one loop iteration, which checks context
	// first and returns context.Canceled without calling SetStatus.
	pc.messages <- &sarama.ConsumerMessage{
		Offset: 0,
		Key:    []byte(string(webhookID)),
		Value:  makeSnapshotBytes(t, snap),
	}

	consumer := new(mockSaramaConsumer)
	consumer.On("ConsumePartition", "howk.state", int32(0), sarama.OffsetOldest).Return(pc, nil)

	var restored, tombstones, parseErrors int64
	err := r.consumePartition(ctx, consumer, 0, &restored, &tombstones, &parseErrors)

	assert.ErrorIs(t, err, context.Canceled)
	// SetStatus is NOT called because context check short-circuits processing.
	mockHS.AssertNotCalled(t, "SetStatus", mock.Anything, mock.Anything)

	consumer.AssertExpectations(t)
	pc.AssertExpectations(t)
}

// TestConsumePartition_HighWaterMarkStopsPartition verifies that processing
// stops once the message offset reaches HWM-1 without consuming further.
func TestConsumePartition_HighWaterMarkStopsPartition(t *testing.T) {
	mockHS := new(MockHotState)
	r := newTestReconciler(mockHS)

	now := time.Now()
	id1 := domain.WebhookID(ulid.Make().String())
	id2 := domain.WebhookID(ulid.Make().String())

	snap1 := domain.WebhookStateSnapshot{
		WebhookID: id1, State: domain.StateDelivered,
		Attempt: 0, MaxAttempts: 10, CreatedAt: now,
	}
	snap2 := domain.WebhookStateSnapshot{
		WebhookID: id2, State: domain.StateDelivered,
		Attempt: 0, MaxAttempts: 10, CreatedAt: now,
	}

	// HWM = 2; offset 1 is the last expected (HWM-1).
	// We put a third message at offset 2 that should never be consumed.
	pc := newMockPartitionConsumer(2)
	pc.On("Close").Return(nil)

	msg0 := &sarama.ConsumerMessage{Offset: 0, Key: []byte(string(id1)), Value: makeSnapshotBytes(t, snap1)}
	msg1 := &sarama.ConsumerMessage{Offset: 1, Key: []byte(string(id2)), Value: makeSnapshotBytes(t, snap2)}
	// msg2 is beyond HWM; must never be processed.
	msg2 := &sarama.ConsumerMessage{Offset: 2, Key: []byte("extra"), Value: []byte("not-valid-json{{{}")}
	sendAndClose(pc, msg0, msg1, msg2)

	consumer := new(mockSaramaConsumer)
	consumer.On("ConsumePartition", "howk.state", int32(0), sarama.OffsetOldest).Return(pc, nil)

	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.WebhookID == id1
	})).Return(nil)
	mockHS.On("SetStatus", mock.Anything, mock.MatchedBy(func(s *domain.WebhookStatus) bool {
		return s.WebhookID == id2
	})).Return(nil)

	var restored, tombstones, parseErrors int64
	err := r.consumePartition(context.Background(), consumer, 0, &restored, &tombstones, &parseErrors)

	assert.NoError(t, err)
	// Only the two messages within HWM must be processed.
	assert.Equal(t, int64(2), restored)
	assert.Equal(t, int64(0), parseErrors, "third message must not be processed")

	mockHS.AssertExpectations(t)
	consumer.AssertExpectations(t)
	pc.AssertExpectations(t)
}
