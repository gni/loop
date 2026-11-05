package run

import (
	"context"
	"strings"
	"sync"

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

type taskInstructions struct {
	mu        sync.RWMutex
	objective string
	updates   []string
}

func WithObjective(ctx context.Context, objective string) context.Context {
	return context.WithValue(ctx, objectiveKey{}, &taskInstructions{objective: strings.TrimSpace(objective)})
}

func WithoutTaskInstructions(ctx context.Context) context.Context {
	return context.WithValue(ctx, objectiveKey{}, (*taskInstructions)(nil))
}

func Objective(ctx context.Context) string {
	state, _ := ctx.Value(objectiveKey{}).(*taskInstructions)
	if state == nil {
		return ""
	}
	return state.objective
}

// AddInstruction preserves explicit steering exactly; it does not infer goals
// or constraints from tool output. Each delegated task has its own state.
func AddInstruction(ctx context.Context, instruction string) {
	state, _ := ctx.Value(objectiveKey{}).(*taskInstructions)
	instruction = strings.TrimSpace(instruction)
	if state == nil || instruction == "" {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if instruction == state.objective {
		return
	}
	for _, previous := range state.updates {
		if previous == instruction {
			return
		}
	}
	state.updates = append(state.updates, instruction)
}

func Instructions(ctx context.Context) []string {
	state, _ := ctx.Value(objectiveKey{}).(*taskInstructions)
	if state == nil {
		return nil
	}
	state.mu.RLock()
	defer state.mu.RUnlock()
	var instructions []string
	if state.objective != "" {
		instructions = append(instructions, state.objective)
	}
	return append(instructions, state.updates...)
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
