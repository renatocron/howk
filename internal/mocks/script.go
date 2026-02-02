package mocks

import (
	"context"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
	"github.com/stretchr/testify/mock"
)

// MockValidator implements script.ValidatorInterface for testing
type MockValidator struct {
	mock.Mock
}

func (m *MockValidator) ValidateSyntax(luaCode string) error {
	args := m.Called(luaCode)
	return args.Error(0)
}

// MockPublisher implements script.PublisherInterface for testing
type MockPublisher struct {
	mock.Mock
}

func (m *MockPublisher) PublishScript(ctx context.Context, s *script.ScriptConfig) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *MockPublisher) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	args := m.Called(ctx, configID)
	return args.Error(0)
}

// Compile-time assertions
var _ script.ValidatorInterface = (*MockValidator)(nil)
var _ script.PublisherInterface = (*MockPublisher)(nil)
