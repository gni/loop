package agent

import (
	"context"
	"fmt"
	"io"
	"time"

	"loop/pkg/agent/run"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (a *Agent) appendTurnInput(messages *[]db.Message, prompt, sessionID string) {
	if pending := a.TakePendingSystemEvent(); pending != "" {
		prompt = fmt.Sprintf("[%s]\n\n%s", pending, prompt)
	}
	a.SyncSessionSystemPrompt(messages, sessionID)

	userMsg := db.Message{Role: "user", ContextKind: "objective", Content: prompt, ContentBlocks: a.InputBlocks, TaskState: run.TaskSnapshot(a.Context()), InheritedInstructions: run.DelegationInstructions(a.Context())}
	a.MessagesMu.Lock()
	*messages = append(*messages, userMsg)
	a.MessagesMu.Unlock()

	if sessionID != "" {
		if !a.HasMessages(sessionID) {
			// Draft-only reference instructions become durable with the first
			// real input, alongside the leading prompt and accepted message.
			if err := a.CheckpointSession(a.Context(), sessionID, *messages); err != nil {
				a.recordPersistenceError(err)
			}
		} else if err := a.SaveMessage(sessionID, userMsg); err != nil {
			a.recordPersistenceError(err)
		}
	}

	a.emit(Event{Type: "message_end", SessionID: sessionID, Message: &userMsg})
	a.InputBlocks = nil
	a.DebugLogUserCommand(sessionID, prompt)
}

func (a *Agent) prepareAgentStep(ctx context.Context, messages *[]db.Message, allowlist []string, sessionID string, iter int, previousTrailingNewlines int, engine *TurnEngine, sink HistorySink, timer *TimingRecorder, w io.Writer, theme style.UITheme) (globalPromptTokensEst, effectiveLimit int, err error) {
	engine.BeginTurn()

	globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(*messages, allowlist)
	compactionDue := len(*messages) > 4 && a.CompactionTrigger(globalPromptTokensEst, 0, effectiveLimit)
	timer.Time("compaction", "start")
	globalPromptTokensEst, effectiveLimit, err = a.maybeCompress(ctx, messages, sessionID, theme, w, allowlist, globalPromptTokensEst, 0, effectiveLimit)
	if err != nil {
		return 0, 0, err
	}
	if compactionDue {
		engine.HistoryCompacted()
	}
	if err := a.persistenceError(); err != nil {
		return 0, 0, err
	}
	timer.Time("compaction", "end")

	if engine.ShouldRecap() {
		engine.MaybeRecap(sink, globalPromptTokensEst, a.GetSessionTotalCompletionTokens(*messages), effectiveLimit, w, theme)
		globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(*messages, allowlist)
	}

	if iter > 1 {
		for lines := previousTrailingNewlines; lines < style.ActivityGapLines+1; lines++ {
			fmt.Fprint(w, "\n")
		}
		fmt.Fprintln(w, style.ActivityDivider(theme))
	}

	return globalPromptTokensEst, effectiveLimit, nil
}

// consumeTurnChunks presents provider events on the caller goroutine. The
// bounded relay absorbs terminal backpressure without executing provisional calls.
func (a *Agent) consumeTurnChunks(ctx context.Context, chunks <-chan StreamChunk, sessionID string, loader *turnLoader, echoFilter *PromptEchoFilter, sr StreamRenderer, toolStream *toolStreamSequencer) {
	stream := relayStreamChunks(ctx, chunks)
	refresh := time.NewTicker(33 * time.Millisecond)
	defer refresh.Stop()
	for {
		var chunk StreamChunk
		select {
		case next, ok := <-stream:
			if !ok {
				return
			}
			chunk = next
		case <-refresh.C:
			a.refreshStreamPresentation(sr)
			continue
		}
		a.emit(Event{Type: "message_update", SessionID: sessionID, Delta: chunk.Content, Kind: chunk.Type})
		switch chunk.Type {
		case "reasoning":
			if loader != nil {
				loader.PauseDots()
				loader.Feed()
			}
			if cleaned := echoFilter.Write(chunk.Content); cleaned != "" {
				sr.WriteReasoning(cleaned)
			}
		case "text":
			if loader != nil {
				loader.PauseDots()
				loader.Feed()
			}
			sr.Write(chunk.Content)
		case "tool_name":
			if loader != nil {
				loader.ShowDots()
				loader.Feed()
			}
			toolStream.Feed(chunk)
		case "tool_target":
			toolStream.Feed(chunk)
		case "tool_call":
			if loader != nil {
				loader.Feed()
			}
			toolStream.Feed(chunk)
		}
		a.refreshStreamPresentation(sr)
	}
}

// refreshStreamPresentation runs only on the foreground presentation goroutine.
// Semantic replays preserve incomplete lines; legacy renderers wait for a safe
// boundary before their terminal-byte snapshot is used.
func (a *Agent) refreshStreamPresentation(sr StreamRenderer) {
	if view, ok := sr.(interface{ SetLiveCollapse(bool) }); ok {
		view.SetLiveCollapse(a.Config.CollapseResults)
	}
	if view, ok := sr.(interface{ SafeToReplay() bool }); ok && !view.SafeToReplay() {
		return
	}
	if a.OnStreamProgress != nil && a.OnStreamProgress() {
		if view, ok := sr.(interface{ InvalidateTrackedRows() }); ok {
			view.InvalidateTrackedRows()
		}
	}
}

func (a *Agent) runTurnToolCalls(ctx context.Context, engine *TurnEngine, batch *ToolBatch, toolStream *toolStreamSequencer, assistantMsg *db.Message, sequentialDisplay bool, sink HistorySink, sr StreamRenderer, loader *turnLoader, ncw *NewlineCounterWriter, theme style.UITheme, sessionID string, iter int) (halted, aborted bool) {
	engine.SetToolBatch(batch)
	if sequentialDisplay {
		engine.SetBeforeToolCall(toolStream.ReplayCall)
	}
	logCall := func(tc db.ToolCall, output string, toolErr error) {
		a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
	}
	halted, aborted = engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, loader, ncw, theme, NewMainRenderer(a), logCall)
	engine.SetBeforeToolCall(nil)
	engine.SetToolBatch(nil)
	if halted || aborted {
		batch.CancelUnfinished()
	}
	return halted, aborted
}

// renderFinalAgentTurn completes the presentation of the final assistant reply.
func (a *Agent) renderFinalAgentTurn(assistantMsg *db.Message, sr StreamRenderer, loader *turnLoader, rawW io.Writer, w io.Writer, sessionID string, startTime time.Time, theme style.UITheme) {
	if pending, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
		pending.MarkPendingWriteResults("Not executed")
	}
	if loader != nil {
		loader.Stop()
	}
	timeStyled := formatTurnElapsed(startTime, theme, a.Config.ShowMicroseconds)

	statsText := timeStyled
	if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
		statsText = formatCompletionTokensBadge(assistantMsg.CompletionTokens, theme) + timeStyled
	}

	_, height := style.GetTerminalSize()
	if height > 0 && a.UI != nil {
		a.UI.DrawStatsLine(rawW, theme, "", statsText)
	} else {
		fmt.Fprintln(w, statsText)
	}

	if dropped := a.TurnDroppedChunks(); dropped > 0 && a.UI != nil {
		a.UI.RenderGenerationError(w, fmt.Sprintf("output truncated: %d stream chunk(s) could not be delivered to the UI", dropped), theme)
	}

	return
}
