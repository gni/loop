package swarm

import (
	"context"
	"fmt"
	"io"

	"loop/pkg/agent"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

func (ma *MultiAgent) compressIfNeeded(ctx context.Context, w io.Writer, theme style.UITheme, engine *agent.TurnEngine) (bool, error) {
	history := newSwarmSink(ma).Messages()
	if len(history) <= 4 {
		return false, nil
	}
	// Prompt usage already measures the next request; accumulated completion
	// tokens must not trigger repeated compaction of a small current context.
	promptTokens, _ := ma.BaseAgent.GetGlobalTokens(history, ma.GetToolAllowlist())
	effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(promptTokens)
	if engine != nil && engine.CompactionCooldownActive(promptTokens, effectiveLimit) {
		return false, nil
	}
	if !ma.BaseAgent.AutoCompactionDue(history, promptTokens, 0, effectiveLimit) {
		return false, nil
	}
	compacted, err := ma.compactHistory(ctx, w, theme)
	if err != nil {
		return false, err
	}
	if !compacted {
		if engine != nil {
			engine.NoteCompactionNoOp(promptTokens)
		}
		return false, nil
	}
	if engine != nil {
		engine.ClearCompactionCooldown()
	}
	return true, nil
}

func (ma *MultiAgent) compactHistory(ctx context.Context, w io.Writer, theme style.UITheme) (bool, error) {
	ma.HistoryMu.RLock()
	history := message.CloneAll(ma.History)
	snapshotLen := len(ma.History)
	ma.HistoryMu.RUnlock()
	if snapshotLen <= 4 {
		return false, fmt.Errorf("subagent input exceeds the local model context")
	}
	compacted, err := ma.BaseAgent.CompressHistoryWithOutcome(ctx, &history, "", theme, w)
	if err != nil || !compacted {
		return compacted, err
	}
	ma.HistoryMu.Lock()
	history = append(history, ma.History[snapshotLen:]...)
	ma.History = history
	ma.HistoryMu.Unlock()
	return true, nil
}
