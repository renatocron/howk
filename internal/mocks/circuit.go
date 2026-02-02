package mocks

import (
	"context"

	"github.com/howk/howk/internal/domain"
	"github.com/stretchr/testify/mock"
)

// MockCircuitBreaker implements hotstate.CircuitBreakerChecker interface for testing
type MockCircuitBreaker struct {
	mock.Mock
}

func (m *MockCircuitBreaker) Get(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreaker) ShouldAllow(ctx context.Context, endpointHash domain.EndpointHash) (bool, bool, error) {
	args := m.Called(ctx, endpointHash)
	return args.Bool(0), args.Bool(1), args.Error(2)
}

func (m *MockCircuitBreaker) RecordSuccess(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}

func (m *MockCircuitBreaker) RecordFailure(ctx context.Context, endpointHash domain.EndpointHash) (*domain.CircuitBreaker, error) {
	args := m.Called(ctx, endpointHash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*domain.CircuitBreaker), args.Error(1)
}
