package run

import (
	"context"

	"loop/pkg/domain/message"
	"loop/pkg/domain/tool"
)

// Event contains semantic lifecycle state, never terminal escape sequences.
type Event struct {
	Type    string
	Step    int
	Message *message.Message
}

type objectiveKey struct{}

func WithObjective(ctx context.Context, objective string) context.Context {
	return context.WithValue(ctx, objectiveKey{}, objective)
}

func Objective(ctx context.Context) string {
	objective, _ := ctx.Value(objectiveKey{}).(string)
	return objective
}

type planKey struct{}

func WithPlan(ctx context.Context, get func() []tool.TodoItem) context.Context {
	return context.WithValue(ctx, planKey{}, get)
}

func Plan(ctx context.Context, fallback func() []tool.TodoItem) []tool.TodoItem {
	if get, ok := ctx.Value(planKey{}).(func() []tool.TodoItem); ok {
		return get()
	}
	return fallback()
}
