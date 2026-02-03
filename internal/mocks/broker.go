package mocks

import (
	"context"

	"github.com/howk/howk/internal/broker"
	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/mock"
)

// MockWebhookPublisher implements broker.WebhookPublisher for testing
type MockWebhookPublisher struct {
	mock.Mock
}

func (m *MockWebhookPublisher) PublishWebhook(ctx context.Context, webhook *domain.Webhook) error {
	args := m.Called(ctx, webhook)
	return args.Error(0)
}

func (m *MockWebhookPublisher) PublishResult(ctx context.Context, result *domain.DeliveryResult) error {
	args := m.Called(ctx, result)
	return args.Error(0)
}

func (m *MockWebhookPublisher) PublishDeadLetter(ctx context.Context, dl *domain.DeadLetter) error {
	args := m.Called(ctx, dl)
	return args.Error(0)
}

func (m *MockWebhookPublisher) Close() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockWebhookPublisher) PublishToSlow(ctx context.Context, webhook *domain.Webhook) error {
	args := m.Called(ctx, webhook)
	return args.Error(0)
}

// MockBroker implements broker.Broker for testing
type MockBroker struct {
	mock.Mock
}

func (m *MockBroker) Publish(ctx context.Context, topic string, msgs ...broker.Message) error {
	args := m.Called(ctx, topic, msgs)
	return args.Error(0)
}

func (m *MockBroker) Subscribe(ctx context.Context, topic, group string, handler broker.Handler) error {
	args := m.Called(ctx, topic, group, handler)
	return args.Error(0)
}

func (m *MockBroker) Close() error {
	args := m.Called()
	return args.Error(0)
}

// MockPinger is an optional interface for publishers that support Ping
type MockPinger struct {
	mock.Mock
}

func (m *MockPinger) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Compile-time assertions
var _ broker.WebhookPublisher = (*MockWebhookPublisher)(nil)
var _ broker.Broker = (*MockBroker)(nil)
