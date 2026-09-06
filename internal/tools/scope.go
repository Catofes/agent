package tools

import "context"

type executionScopeKey struct{}

type ExecutionScope struct {
	RunID          string
	StudentID      string
	ConversationID string
	TurnID         string
}

func WithExecutionScope(ctx context.Context, scope ExecutionScope) context.Context {
	return context.WithValue(ctx, executionScopeKey{}, scope)
}

func ExecutionScopeFrom(ctx context.Context) (ExecutionScope, bool) {
	scope, ok := ctx.Value(executionScopeKey{}).(ExecutionScope)
	return scope, ok
}
