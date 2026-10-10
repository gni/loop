package swarm

import (
	"context"
	"io"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// compressIfNeeded summarizes older subagent turns when the conversation approaches
// the effective context limit, mirroring what the main agent loop does. Subagents
// previously grew unbounded: pkg/agent/request.go silently drops whole oldest turns
// once the estimate exceeds the limit, so a long-running subagent loses its early
// context without ever producing a summary.
//
// Compression runs on a copy outside HistoryMu (it makes a network call), then the
// compacted history is swapped in under the lock so persisted state and the next
// iteration both see it. Session rewriting is skipped via an empty sessionID: subagent
// state is persisted through the manager, not the session store.
func (ma *MultiAgent) compressIfNeeded(ctx context.Context, w io.Writer, theme style.UITheme) {
	if ma == nil || ma.BaseAgent == nil {
		return
	}

	ma.HistoryMu.RLock()
	history := make([]db.Message, len(ma.History))
	copy(history, ma.History)
	snapshotLen := len(ma.History)
	ma.HistoryMu.RUnlock()

	pTokens, _ := ma.BaseAgent.GetGlobalTokens(history, ma.GetToolAllowlist())
	effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(pTokens)

	thresh := ma.BaseAgent.CompressionThreshold()

	if len(history) <= 4 || pTokens < int(thresh*float64(effectiveLimit)) {
		return
	}

	ma.BaseAgent.CompressHistory(ctx, &history, "", theme, w)

	// Re-take the write lock and preserve messages appended during the network call
	// (Start() appends concurrently). Without this, `ma.History = history` would drop
	// that delta.
	ma.HistoryMu.Lock()
	var delta []db.Message
	if snapshotLen <= len(ma.History) {
		delta = ma.History[snapshotLen:]
	}
	if len(delta) > 0 {
		merged := make([]db.Message, 0, len(history)+len(delta))
		merged = append(merged, history...)
		merged = append(merged, delta...)
		ma.History = merged
	} else {
		ma.History = history
	}
	ma.HistoryMu.Unlock()
}

// pruneToolOutput applies the same deterministic output pruning and spill-to-disk the
// main agent loop uses, so a subagent cannot blow its own context budget with verbose
// tool output. Returns the pruned representation, or the original output when it fits.
func (ma *MultiAgent) pruneToolOutput(toolCallID, toolName, output string) string {
	if ma == nil || ma.BaseAgent == nil {
		return output
	}
	maxOutputBytes := tool.DefaultMaxToolOutputBytes
	if ma.BaseAgent.Config != nil && ma.BaseAgent.Config.MaxToolOutputBytes > 0 {
		maxOutputBytes = ma.BaseAgent.Config.MaxToolOutputBytes
	}
	pruned, _, prunedFlag := tool.SpillAndPruneOutput(ma.BaseAgent.GetWorkspaceRoot(), "subagent:"+ma.Name, toolCallID, toolName, output, maxOutputBytes)
	if prunedFlag && pruned != "" {
		return pruned
	}
	return output
}
