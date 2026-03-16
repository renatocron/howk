package mocks

import (
	"context"

	"github.com/howk/howk/internal/domain"
	"github.com/howk/howk/internal/script"
	"github.com/stretchr/testify/mock"
)

// MockValidator implements script.SyntaxChecker for testing
type MockValidator struct {
	mock.Mock
}

func (m *MockValidator) ValidateSyntax(luaCode string) error {
	args := m.Called(luaCode)
	return args.Error(0)
}

// MockPublisher implements script.ScriptPublisher for testing
type MockPublisher struct {
	mock.Mock
}

func (m *MockPublisher) PublishScript(ctx context.Context, s *script.Config) error {
	args := m.Called(ctx, s)
	return args.Error(0)
}

func (m *MockPublisher) DeleteScript(ctx context.Context, configID domain.ConfigID) error {
	args := m.Called(ctx, configID)
	return args.Error(0)
}

// Compile-time assertions
var _ script.SyntaxChecker = (*MockValidator)(nil)
var _ script.ScriptPublisher = (*MockPublisher)(nil)
