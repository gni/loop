package swarm

import (
	"context"
	"fmt"
	"io"

	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

func (ma *MultiAgent) compressIfNeeded(ctx context.Context, w io.Writer, theme style.UITheme) error {
	history := newSwarmSink(ma).Messages()
	if len(history) <= 4 {
		return nil
	}
	// Prompt usage already measures the next request; accumulated completion
	// tokens must not trigger repeated compaction of a small current context.
	promptTokens, _ := ma.BaseAgent.GetGlobalTokens(history, ma.GetToolAllowlist())
	effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(promptTokens)
	if !ma.BaseAgent.CompactionTrigger(promptTokens, 0, effectiveLimit) {
		return nil
	}
	return ma.compactHistory(ctx, w, theme)
}

func (ma *MultiAgent) compactHistory(ctx context.Context, w io.Writer, theme style.UITheme) error {
	ma.HistoryMu.RLock()
	history := message.CloneAll(ma.History)
	snapshotLen := len(ma.History)
	ma.HistoryMu.RUnlock()
	if snapshotLen <= 4 {
		return fmt.Errorf("subagent input exceeds the local model context")
	}
	if err := ma.BaseAgent.CompressHistory(ctx, &history, "", theme, w); err != nil {
		return err
	}
	ma.HistoryMu.Lock()
	history = append(history, ma.History[snapshotLen:]...)
	ma.History = history
	ma.HistoryMu.Unlock()
	return nil
}
