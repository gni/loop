package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"loop/pkg/agent/fallback"
	"loop/pkg/db"
)

const maxResponseBytes = 16 << 20
const maxResponseToolCalls = fallback.MaxToolCalls

// responseBudget bounds retained output independently of per-event wire limits.
type responseBudget int

// Incremental arguments use builders so token-sized fragments stay linear in
// the response size. Message strings are published only at snapshot boundaries.
type toolArgumentBuffers map[int]*strings.Builder

func (b toolArgumentBuffers) append(index int, fragment string) {
	if b[index] == nil {
		b[index] = &strings.Builder{}
	}
	b[index].WriteString(fragment)
}

func (b toolArgumentBuffers) replace(index int, arguments string) {
	b[index] = &strings.Builder{}
	b[index].WriteString(arguments)
}

func (b toolArgumentBuffers) flush(calls map[int]*db.ToolCall) {
	for index, buffer := range b {
		if call := calls[index]; call != nil {
			call.Function.Arguments = buffer.String()
			if strings.TrimSpace(call.Function.Arguments) == "" {
				call.Function.Arguments = "{}"
			}
		}
	}
}

func (b *responseBudget) add(parts ...string) error {
	for _, part := range parts {
		if len(part) > maxResponseBytes-int(*b) {
			return fmt.Errorf("provider response exceeds 16 MiB output limit")
		}
		*b += responseBudget(len(part))
	}
	return nil
}

func validateProviderCalls(calls map[int]*db.ToolCall) error {
	if len(calls) > maxResponseToolCalls {
		return fmt.Errorf("provider response exceeds %d tool calls", maxResponseToolCalls)
	}
	for _, call := range calls {
		if strings.TrimSpace(call.Function.Name) == "" {
			return fmt.Errorf("provider returned a tool call without a name")
		}
		args := strings.TrimSpace(call.Function.Arguments)
		if args == "" {
			call.Function.Arguments = "{}"
			continue
		}
		if !strings.HasPrefix(args, "{") || !json.Valid([]byte(args)) {
			return fmt.Errorf("invalid or incomplete arguments for tool %q: %w", call.Function.Name, ErrIncompleteResponse)
		}
	}
	return nil
}
