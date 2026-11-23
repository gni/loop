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
	a.MessagesMu.RLock()
	run.RestoreTask(a.Context(), latestHistoricalTask(*messages))
	a.MessagesMu.RUnlock()
	if pending := a.TakePendingSystemEvent(); pending != "" {
		prompt = fmt.Sprintf("[%s]\n\n%s", pending, prompt)
	}
	// Optional knowledge is selected once for the new task, not on every step.
	a.markSystemPromptDirty()
	a.SyncSessionSystemPrompt(messages, sessionID)

	userMsg := db.Message{Role: "user", ContextKind: "objective", Content: prompt, ContentBlocks: a.InputBlocks, TaskState: run.TaskSnapshot(a.Context()), InheritedInstructions: run.DelegationInstructions(a.Context())}
	a.MessagesMu.Lock()
	*messages = append(*messages, userMsg)
	checkpointMsgs := append([]db.Message(nil), (*messages)...)
	a.MessagesMu.Unlock()

	if sessionID != "" {
		if !a.HasMessages(sessionID) {
			// Draft-only reference instructions become durable with the first
			// real input, alongside the leading prompt and accepted message.
			if err := a.CheckpointSession(a.Context(), sessionID, checkpointMsgs); err != nil {
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

	a.MessagesMu.RLock()
	stepSnapshot := append([]db.Message(nil), (*messages)...)
	var beforeFirst *db.Message
	if len(*messages) > 0 {
		beforeFirst = &(*messages)[0]
	}
	beforeLen := len(*messages)
	a.MessagesMu.RUnlock()

	globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(stepSnapshot, allowlist)
	timer.Time("compaction", "start")
	globalPromptTokensEst, effectiveLimit, err = a.maybeCompress(ctx, messages, sessionID, theme, w, allowlist, globalPromptTokensEst, 0, effectiveLimit)
	if err != nil {
		return 0, 0, err
	}
	a.MessagesMu.RLock()
	compacted := len(*messages) != beforeLen || (beforeLen > 0 && &(*messages)[0] != beforeFirst)
	a.MessagesMu.RUnlock()
	if compacted {
		engine.HistoryCompacted()
	}
	if err := a.persistenceError(); err != nil {
		return 0, 0, err
	}
	timer.Time("compaction", "end")

	if engine.ShouldRecap() {
		a.MessagesMu.RLock()
		recapSnapshot := append([]db.Message(nil), (*messages)...)
		a.MessagesMu.RUnlock()
		engine.MaybeRecap(sink, globalPromptTokensEst, a.GetSessionTotalCompletionTokens(recapSnapshot), effectiveLimit, w, theme)
		a.MessagesMu.RLock()
		postRecap := append([]db.Message(nil), (*messages)...)
		a.MessagesMu.RUnlock()
		globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(postRecap, allowlist)
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
	if view, ok := sr.(LiveCollapsePresenter); ok {
		view.SetLiveCollapse(a.LiveSettings().CollapseResults)
	}
	if view, ok := sr.(ReplaySafety); ok && !view.SafeToReplay() {
		return
	}
	if a.OnStreamProgress != nil && a.OnStreamProgress() {
		if view, ok := sr.(TrackedRowInvalidator); ok {
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
	if pending, ok := sr.(PendingWritePresenter); ok {
		pending.MarkPendingWriteResults("Not executed")
	}
	if loader != nil {
		loader.Stop()
	}
	live := a.LiveSettings()
	timeStyled := formatTurnElapsed(startTime, theme, live.ShowMicroseconds)

	statsText := timeStyled
	if live.ShowTokens && assistantMsg.CompletionTokens > 0 {
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
