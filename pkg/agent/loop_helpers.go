package agent

import (
	"context"
	"fmt"
	"io"
	"loop/pkg/db"
	"loop/pkg/ui/style"
	"time"
)

func (a *Agent) appendTurnInput(messages *[]db.Message, prompt, sessionID string) {
	a.SyncSessionSystemPrompt(messages, sessionID)

	userMsg := db.Message{Role: "user", Content: prompt, ContentBlocks: a.InputBlocks}
	a.MessagesMu.Lock()
	*messages = append(*messages, userMsg)
	var firstSys *db.Message
	if len(*messages) > 1 && (*messages)[0].Role == "system" {
		sysCopy := (*messages)[0]
		firstSys = &sysCopy
	}
	a.MessagesMu.Unlock()

	if sessionID != "" {
		if !a.HasMessages(sessionID) && firstSys != nil {
			if err := a.SaveMessage(sessionID, *firstSys); err != nil {
				a.recordPersistenceError(err)
			}
		}
		if err := a.SaveMessage(sessionID, userMsg); err != nil {
			a.recordPersistenceError(err)
		}
	}

	a.emit(Event{Type: "message_end", SessionID: sessionID, Message: &userMsg})
	a.InputBlocks = nil
	a.DebugLogUserCommand(sessionID, prompt)
}

func (a *Agent) prepareAgentStep(ctx context.Context, messages *[]db.Message, allowlist []string, sessionID string, iter int, previousTrailingNewlines int, engine *TurnEngine, sink HistorySink, timer *TimingRecorder, w io.Writer, theme style.UITheme) (globalPromptTokensEst, effectiveLimit int, err error) {
	if iter > 1 {
		a.SyncSessionSystemPrompt(messages, sessionID)
	}

	engine.BeginTurn()

	globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(*messages, allowlist)
	timer.Time("compaction", "start")
	globalPromptTokensEst, effectiveLimit, err = a.maybeCompress(ctx, messages, sessionID, theme, w, allowlist, globalPromptTokensEst, 0, effectiveLimit)
	if err != nil {
		return 0, 0, err
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

// consumeTurnChunks renders on the caller goroutine; the relay drains the provider
// while a synchronous early write/edit is running. Renderer and engine remain
// serialized, preserving tool call ordering and pairing.
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

// reconcileEarlyToolCalls compares the authoritative provider response with
// speculative write/edit calls before remapping provisional IDs. A mismatch
// must never lead to executing a side effect twice.
func reconcileEarlyToolCalls(assistantMsg *db.Message, earlyCalls []db.ToolCall, earlyMessages []db.Message) bool {
	// Calls already executed during generation must still agree with
	// the final provider response. Never execute a mismatched call twice.
	mismatch := len(earlyCalls) > len(assistantMsg.ToolCalls)
	if !mismatch {
		for i, early := range earlyCalls {
			final := assistantMsg.ToolCalls[i]
			if early.Function.Name != final.Function.Name ||
				SanitizeLLMControlTokens(early.Function.Arguments) != final.Function.Arguments {
				mismatch = true
				break
			}
		}
	}
	if mismatch {
		return false
	}
	for i := range earlyCalls {
		for j := range earlyMessages {
			if earlyMessages[j].ToolCallID == earlyCalls[i].ID {
				earlyMessages[j].ToolCallID = assistantMsg.ToolCalls[i].ID
			}
		}
	}
	return true
}

func (a *Agent) runRemainingToolCalls(ctx context.Context, engine *TurnEngine, batch *ToolBatch, toolStream *toolStreamSequencer, assistantMsg *db.Message, earlyCalls []db.ToolCall, sequentialDisplay bool, sink HistorySink, sr StreamRenderer, loader *turnLoader, ncw *NewlineCounterWriter, theme style.UITheme, sessionID string, iter int) (halted, aborted bool) {
	engine.SetToolBatch(batch)
	if sequentialDisplay {
		engine.SetBeforeToolCall(toolStream.ReplayCall)
	}
	logCall := func(tc db.ToolCall, output string, toolErr error) {
		a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
	}
	if len(earlyCalls) == 0 {
		halted, aborted = engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, loader, ncw, theme, NewMainRenderer(a), logCall)
	} else {
		// Earlier calls already ran and were persisted above. Execute
		// only the remaining indexes in model order (never twice).
		for idx := len(earlyCalls); idx < len(assistantMsg.ToolCalls); idx++ {
			tc := assistantMsg.ToolCalls[idx]
			halted, aborted = engine.ExecuteToolCallAt(ctx, idx, tc, assistantMsg.ToolCalls[idx:], assistantMsg.Content, sink, sr, loader, ncw, theme, NewMainRenderer(a), logCall)
			if halted || aborted {
				if aborted {
					engine.RecordInterruption(sink, assistantMsg.ToolCalls[idx+1:], "aborted by user")
				}
				break
			}
		}
		// This path bypasses ExecuteToolCalls' deferred cleanup. Any
		// preview left by an interrupted call must not imply success.
		if marker, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
			marker.MarkPendingWriteResults("Not executed")
		}
	}
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
	a.emit(Event{Type: "turn_end", SessionID: sessionID, Message: assistantMsg})
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
