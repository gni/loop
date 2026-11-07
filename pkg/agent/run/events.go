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
	inherited []string
}

type inheritedInstructionsKey struct{}

// WithInheritedInstructions carries exact user instructions, never summaries
// extracted from tool output. Each child receives an independent slice.
func WithInheritedInstructions(ctx context.Context, instructions []string) context.Context {
	return context.WithValue(ctx, inheritedInstructionsKey{}, append([]string(nil), instructions...))
}

func WithObjective(ctx context.Context, objective string) context.Context {
	inherited, _ := ctx.Value(inheritedInstructionsKey{}).([]string)
	return context.WithValue(ctx, objectiveKey{}, &taskInstructions{objective: strings.TrimSpace(objective), inherited: append([]string(nil), inherited...)})
}

func WithoutTaskInstructions(ctx context.Context) context.Context {
	return WithoutTask(context.WithValue(ctx, objectiveKey{}, (*taskInstructions)(nil)))
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
	reopenTask(ctx)
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
	for _, inherited := range state.inherited {
		duplicate := false
		for _, present := range instructions {
			if present == inherited {
				duplicate = true
				break
			}
		}
		if !duplicate {
			instructions = append(instructions, inherited)
		}
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
