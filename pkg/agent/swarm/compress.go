package swarm

import (
	"context"
	"io"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (ma *MultiAgent) compressIfNeeded(ctx context.Context, w io.Writer, theme style.UITheme) {
	if ma == nil || ma.BaseAgent == nil {
		return
	}

	ma.HistoryMu.RLock()
	history := make([]db.Message, len(ma.History))
	copy(history, ma.History)
	snapshotLen := len(ma.History)
	ma.HistoryMu.RUnlock()

	if len(history) <= 4 {
		return
	}

	pTokens, cTokens := ma.BaseAgent.GetGlobalTokens(history, ma.GetToolAllowlist())
	effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(pTokens)
	if !ma.BaseAgent.CompactionTrigger(pTokens, cTokens, effectiveLimit) {
		return
	}

	ma.BaseAgent.CompressHistory(ctx, &history, "", theme, w)

	ma.HistoryMu.Lock()
	if snapshotLen <= len(ma.History) && len(ma.History) > snapshotLen {
		delta := ma.History[snapshotLen:]
		merged := make([]db.Message, 0, len(history)+len(delta))
		merged = append(merged, history...)
		merged = append(merged, delta...)
		ma.History = merged
	} else {
		ma.History = history
	}
	ma.HistoryMu.Unlock()
}

func (ma *MultiAgent) pruneToolOutput(toolCallID, toolName, output string) string {
	if ma == nil || ma.BaseAgent == nil {
		return output
	}
	maxOutputBytes := tool.DefaultMaxToolOutputBytes
	if ma.BaseAgent.Config != nil && ma.BaseAgent.Config.MaxToolOutputBytes > 0 {
		maxOutputBytes = ma.BaseAgent.Config.MaxToolOutputBytes
	}
	if pruned, _, prunedFlag := tool.SpillAndPruneOutput(ma.BaseAgent.GetWorkspaceRoot(), "subagent:"+ma.Name, toolCallID, toolName, output, maxOutputBytes); prunedFlag && pruned != "" {
		return pruned
	}
	return output
}