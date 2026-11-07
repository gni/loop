package run

import (
	"context"
	"strings"
)

type delegationInstructionsKey struct{}

func WithDelegationInstructions(ctx context.Context, instructions []string) context.Context {
	return context.WithValue(ctx, delegationInstructionsKey{}, append([]string(nil), instructions...))
}

// DelegationInstructions retains prior explicit user turns as constraints and
// context, then appends the active instructions. The delegated task defines scope.
// Keep the last occurrence so repeated instructions retain their latest priority.
func DelegationInstructions(ctx context.Context) []string {
	prior, _ := ctx.Value(delegationInstructionsKey{}).([]string)
	all := append(append([]string(nil), prior...), Instructions(ctx)...)
	last := make(map[string]int, len(all))
	for i, s := range all {
		all[i] = strings.TrimSpace(s)
		last[all[i]] = i
	}
	var result []string
	for i, s := range all {
		if s != "" && last[s] == i {
			result = append(result, s)
		}
	}
	return result
}
