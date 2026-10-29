package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func formatTurnElapsed(startTime time.Time, theme style.UITheme) string {
	elapsed := time.Since(startTime)
	timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
	return style.NewStyle().Foreground(theme.Border).Render(timeStr)
}

func formatCompletionTokensBadge(tokens int, theme style.UITheme) string {
	currentCStr := fmt.Sprintf("%d out", tokens)
	if tokens >= 1000 {
		currentCStr = fmt.Sprintf("%.1fk out", float64(tokens)/1000.0)
	}
	cStyled := style.NewStyle().Foreground(theme.Highlight).Render(currentCStr)
	dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")
	return cStyled + dotStyled
}

func (a *Agent) renderTurnError(w io.Writer, ncw io.Writer, sink HistorySink, err error, ctxErr error, isNonInteractive bool, theme style.UITheme) {
	if ctxErr != nil {
		if !isNonInteractive {
			fmt.Fprintln(w)
			cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
			fmt.Fprintln(w, cancelStyle.Render("[operation cancelled]"))
		}
		return
	}

	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	if isNonInteractive {
		fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
		return
	}

	fmt.Fprintln(w)
	if a.UI != nil {
		a.UI.RenderGenerationError(w, err.Error(), theme)
	} else {
		fmt.Fprintf(w, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
	}
	sink.Append(db.Message{Role: "error", Content: err.Error()})
}

func (a *Agent) RunAgentLoop(ctx context.Context, w io.Writer, messages *[]db.Message, prompt string, allowlist []string, theme style.UITheme, isNonInteractive bool, sessionID string) (result RunResult, runErr error) {
	result.Status = RunCompleted
	startedAt := len(*messages)
	a.stateMu.Lock()
	a.persistenceErr = nil
	a.stateMu.Unlock()
	a.emit(Event{Type: "agent_start", SessionID: sessionID})
	defer func() {
		if runErr == nil {
			runErr = a.persistenceError()
		}
		if runErr != nil {
			result.Error = runErr.Error()
			if result.Status == RunCompleted {
				result.Status = RunFailed
			}
			if errors.Is(runErr, context.Canceled) {
				result.Status = RunCancelled
			}
		}
		if startedAt <= len(*messages) {
			result.Messages = append([]db.Message(nil), (*messages)[startedAt:]...)
		}
		a.emit(Event{Type: "agent_end", SessionID: sessionID, Result: &result})
	}()
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	if a.TurnStartTime.IsZero() {
		a.TurnStartTime = time.Now()
	}
	startTime := a.TurnStartTime
	defer func() {
		a.TurnStartTime = time.Time{}
	}()

	writerToUse := w
	rawW := UnwrapWriter(writerToUse)
	a.SetTurnState(writerToUse, ctx, theme)
	defer a.ClearTurnState()

	if a.Guided != nil {
		a.Guided.ResetForNewUserTurn(a.GetTodos())
	}

	var loader *turnLoader
	if a.UI != nil && !isNonInteractive {
		loader = a.newTurnLoader(ctx, rawW, theme, startTime)
		a.CurrentLoader = loader
		defer func() {
			a.CurrentLoader = nil
			loader.Stop()
		}()
	}

	timePrinted := false
	defer func() {
		if !timePrinted {
			fmt.Fprintln(writerToUse)
			if isNonInteractive {
				fmt.Fprintln(writerToUse, formatTurnElapsed(startTime, theme))
			} else {
				// Keep the elapsed-time text on the last occupied scroll row.
				// The reserved row beneath it is already the status-bar gap.
				fmt.Fprint(writerToUse, formatTurnElapsed(startTime, theme))
			}
		}
	}()

	a.SyncSessionSystemPrompt(messages, sessionID)

	*messages = append(*messages, db.Message{Role: "user", Content: prompt, ContentBlocks: a.InputBlocks})
	if sessionID != "" {
		if !a.HasMessages(sessionID) && len(*messages) > 1 && (*messages)[0].Role == "system" {
			if err := a.SaveMessage(sessionID, (*messages)[0]); err != nil {
				a.recordPersistenceError(err)
			}
		}
		if err := a.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
			a.recordPersistenceError(err)
		}
	}

	a.emit(Event{Type: "message_end", SessionID: sessionID, Message: &(*messages)[len(*messages)-1]})
	a.InputBlocks = nil
	a.DebugLogUserCommand(sessionID, prompt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}

	engine := NewTurnEngine(a, EnginePolicy{
		SessionKey:    sessionID,
		ToolAllowlist: func() []string { return allowlist },
		AllowApproval: true,
		RunHooks:      true,
		GuidedMode:    a.IsGuidedMode(),
		ShowRecapLine: !isNonInteractive,
	})
	sink := NewAgentSink(a, messages, sessionID)
	timer := a.Timing
	if timer == nil {
		timer = NewTimingRecorder()
	}
	lastToolTrailingNewlines := 0

	for iter := 1; iter <= maxSteps; iter++ {
		result.Turns = iter
		if err := a.persistenceError(); err != nil {
			runErr = err
			return
		}
		a.emit(Event{Type: "turn_start", SessionID: sessionID})
		if a.NextInput != nil {
			for _, input := range a.NextInput() {
				sink.Append(input)
			}
		}
		if ctx.Err() != nil {
			runErr = ctx.Err()
			return
		}

		if iter > 1 {
			a.SyncSessionSystemPrompt(messages, sessionID)
		}

		engine.BeginTurn()

		globalPromptTokensEst, effectiveLimit := a.estimateTurnContext(*messages, allowlist)
		timer.Time("compaction", "start")
		globalPromptTokensEst, effectiveLimit, _ = a.maybeCompress(ctx, messages, sessionID, theme, writerToUse, allowlist, globalPromptTokensEst, 0, effectiveLimit)
		if err := a.persistenceError(); err != nil {
			runErr = err
			return
		}
		timer.Time("compaction", "end")

		if engine.ShouldRecap() {
			engine.MaybeRecap(sink, globalPromptTokensEst, a.GetSessionTotalCompletionTokens(*messages), effectiveLimit, writerToUse, theme)
			globalPromptTokensEst, effectiveLimit = a.estimateTurnContext(*messages, allowlist)
		}

		if iter > 1 {
			for lines := lastToolTrailingNewlines; lines < style.ActivityGapLines+1; lines++ {
				fmt.Fprint(writerToUse, "\n")
			}
			fmt.Fprintln(writerToUse, style.ActivityDivider(theme))
		}

		toolsForLog := a.Registry.GetAvailableTools(allowlist)
		a.DebugLogLLMRequest(sessionID, iter, a.Config.Model, a.Config.Endpoint, *messages, toolsForLog)

		chunkChan := make(chan StreamChunk, 200)
		type streamResult struct {
			msg *db.Message
			err error
		}
		resChan := make(chan streamResult, 1)

		timer.Time("llm", "start")
		go func() {
			msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, chunkChan)
			close(chunkChan)
			resChan <- streamResult{msg: msg, err: err}
		}()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = new(bytes.Buffer)
		a.CurrentStreamMu.Unlock()

		teeWriter := &CustomTeeWriter{screen: writerToUse, buffer: a.CurrentStreamBuffer}
		ncw := &NewlineCounterWriter{Writer: teeWriter}
		a.SetCurrentWriter(ncw)
		sr := a.NewTurnStreamRenderer(ncw, theme, "loop", prompt)

		echoFilter := NewPromptEchoFilter(prompt)
		priorCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		updateStreamStatus := func(generating bool) {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, effectiveLimit, generating, 0, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		var stopOnce sync.Once
		stopTicker := func() {
			stopOnce.Do(func() {
				updateStreamStatus(false)
			})
		}
		defer stopTicker()

		updateStreamStatus(true)

		// Stream each tool call in index order while keeping interleaved
		// argument fragments from different files separate.
		toolStream := newToolStreamSequencer(sr, a.Config.StreamWrites)
		batch := NewToolBatch(func(update ToolLifecycleEvent) {
			if presenter, ok := sr.(interface{ ObserveToolLifecycle(ToolLifecycleEvent) }); ok {
				presenter.ObserveToolLifecycle(update)
			}
			a.emit(Event{Type: "tool_lifecycle", SessionID: sessionID, Data: update})
		})
		toolStream.SetBatch(batch)
		// The display follows model order in both execution modes. A parallel
		// worker pool may finish out of order, but its headings and results
		// are published together after the batch completes.
		sequentialDisplay := a.Config.StreamWrites
		if sequentialDisplay {
			toolStream.DeferFollowingCalls()
			if presenter, ok := sr.(interface{ SetSequentialToolResults(bool) }); ok {
				presenter.SetSequentialToolResults(true)
			}
		}
		// A completed JSON call can be executed while the model generates
		// subsequent calls. History remains staged until the assistant's final
		// response reconciles provider IDs and arguments.
		var earlyCalls []db.ToolCall
		var earlyMessages []db.Message
		earlyStopped := false
		if a.Config.StreamWrites {
			if presenter, ok := sr.(interface {
				ShowLiveToolContent(int, string, string, string)
				EndLiveToolContent()
			}); ok {
				toolStream.PreviewOnly()
				toolStream.SetLiveContentCallback(presenter.ShowLiveToolContent)
				toolStream.SetEditDiffPreview(func(name, arguments string) string {
					return PreviewToolDiff(a, name, arguments)
				})
				toolStream.SetOnReady(func(index int, name, arguments string) {
					// Only commit a contiguous series of completed write/edit
					// calls. Interleaved, unfinished or unsupported calls continue
					// through the standard post-inference execution engine.
					if earlyStopped || ctx.Err() != nil || index != len(earlyCalls) ||
						(name != "write" && name != "edit") || a.IsGuidedMode() ||
						engine.needsInteractiveApproval(name) {
						return
					}
					call := db.ToolCall{ID: "call_early_" + db.NewUUID(), Type: "function", Function: db.ToolFunction{Name: name, Arguments: arguments}}
					presenter.EndLiveToolContent()
					engine.SetToolBatch(batch)
					engine.SetInlineEarlyResults(true)
					staged := &deferredHistorySink{}
					halted, aborted := engine.ExecuteToolCallAt(ctx, index, call, []db.ToolCall{call}, "", staged, sr, loader, ncw, theme, NewMainRenderer(a), func(tc db.ToolCall, output string, toolErr error) {
						a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
					})
					engine.SetInlineEarlyResults(false)
					earlyCalls = append(earlyCalls, call)
					earlyMessages = append(earlyMessages, staged.messages...)
					if halted || aborted {
						earlyStopped = true
						cancel()
					}
				})
			} else {
				// Keep the original stream mode if the renderer has no live view.
				toolStream = newToolStreamSequencer(sr, true)
				toolStream.SetBatch(batch)
			}
		}
		for chunk := range chunkChan {
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
		}
		timer.Time("llm", "end")

		if pending := echoFilter.Flush(); pending != "" {
			sr.WriteReasoning(pending)
		}
		if loader != nil {
			loader.ShowDots()
		}

		toolStream.Finish()
		if presenter, ok := sr.(interface{ EndLiveToolContent() }); ok {
			presenter.EndLiveToolContent()
		}
		sr.Flush()
		stopTicker()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = nil
		a.CurrentStreamMu.Unlock()

		res := <-resChan
		if res.err != nil && ctx.Err() == nil && isContextOverflow(res.err) && len(*messages) > 4 {
			a.emit(Event{Type: "retry", SessionID: sessionID, Output: "compacting context after provider context rejection"})
			if err := a.CompressHistory(ctx, messages, sessionID, theme, writerToUse); err == nil {
				retryChunks := make(chan StreamChunk, 200)
				retryDone := make(chan streamResult, 1)
				go func() {
					msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, retryChunks)
					close(retryChunks)
					retryDone <- streamResult{msg, err}
				}()
				for chunk := range retryChunks {
					a.emit(Event{Type: "message_update", SessionID: sessionID, Delta: chunk.Content, Kind: chunk.Type})
					if chunk.Type == "text" {
						sr.Write(chunk.Content)
					} else if chunk.Type == "reasoning" {
						sr.WriteReasoning(chunk.Content)
					}
				}
				sr.Flush()
				res = <-retryDone
			} else {
				res.err = errors.Join(res.err, err)
			}
		}
		if res.err != nil {
			batch.CancelUnfinished()
			if pending, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
				pending.MarkPendingWriteResults("Not executed")
			}
			runErr = res.err
			if len(earlyCalls) > 0 {
				// A provider error cannot roll back an already approved file
				// operation. Preserve its tool/assistant history pairing.
				partial := db.Message{Role: "assistant"}
				if res.msg != nil {
					partial = *res.msg
				}
				partial.ToolCalls = earlyCalls
				partial.StopReason = "interrupted"
				sink.Append(partial)
				for _, m := range earlyMessages {
					sink.Append(m)
				}
			} else if res.msg != nil && (res.msg.Content != "" || res.msg.ReasoningContent != "") {
				partial := *res.msg
				partial.ToolCalls = nil
				partial.StopReason = "interrupted"
				sink.Append(partial)
			}
			if loader != nil {
				loader.Stop()
			}
			a.renderTurnError(writerToUse, ncw, sink, res.err, ctx.Err(), isNonInteractive, theme)
			return
		}

		assistantMsg := res.msg
		if assistantMsg == nil {
			batch.CancelUnfinished()
			if len(earlyCalls) > 0 {
				sink.Append(db.Message{Role: "assistant", ToolCalls: earlyCalls, StopReason: "interrupted"})
				for _, m := range earlyMessages {
					sink.Append(m)
				}
			}
			if pending, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
				pending.MarkPendingWriteResults("Not executed")
			}
			runErr = fmt.Errorf("provider returned no assistant response")
			return
		}

		if prompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, prompt)
		}

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
			batch.CancelUnfinished()
			sink.Append(db.Message{Role: "assistant", ToolCalls: earlyCalls, StopReason: "interrupted"})
			for _, m := range earlyMessages {
				sink.Append(m)
			}
			runErr = fmt.Errorf("provider changed a tool call after its completed arguments were executed; refusing remaining calls")
			a.renderTurnError(writerToUse, ncw, sink, runErr, ctx.Err(), isNonInteractive, theme)
			return
		}
		for i := range earlyCalls {
			for j := range earlyMessages {
				if earlyMessages[j].ToolCallID == earlyCalls[i].ID {
					earlyMessages[j].ToolCallID = assistantMsg.ToolCalls[i].ID
				}
			}
		}
		batch.Seal(assistantMsg.ToolCalls)
		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()
		a.DebugLogLLMResponse(sessionID, iter, assistantMsg, a.LastGenerationDuration())
		sink.Append(*assistantMsg)
		for _, m := range earlyMessages {
			sink.Append(m)
		}

		// Compaction is performed immediately before the next inference, when
		// another model call is actually necessary. A final answer must not
		// trigger a separate summarization request.
		globalPromptTokens, postEffectiveLimit := a.estimateTurnContext(*messages, allowlist)
		globalCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		var finalTps float64
		if dur := a.LastGenerationDuration(); dur > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / dur.Seconds()
		}

		syncPostTurnStatus := func() {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, postEffectiveLimit, false, finalTps, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if pending, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
				pending.MarkPendingWriteResults("Not executed")
			}
			a.emit(Event{Type: "turn_end", SessionID: sessionID, Message: assistantMsg})
			result.Text = assistantMsg.Content
			if loader != nil {
				loader.Stop()
			}
			timePrinted = true
			timeStyled := formatTurnElapsed(startTime, theme)

			statsText := timeStyled
			if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
				statsText = formatCompletionTokensBadge(assistantMsg.CompletionTokens, theme) + timeStyled
			}

			_, height := style.GetTerminalSize()
			if height > 0 && a.UI != nil {
				a.UI.DrawStatsLine(rawW, theme, "", statsText)
			} else {
				fmt.Fprintln(writerToUse, statsText)
			}

			if dropped := a.TurnDroppedChunks(); dropped > 0 && a.UI != nil {
				a.UI.RenderGenerationError(writerToUse, fmt.Sprintf("output truncated: %d stream chunk(s) could not be delivered to the UI", dropped), theme)
			}

			syncPostTurnStatus()
			return
		}

		syncPostTurnStatus()

		timer.Time("tool", "start")
		engine.SetToolBatch(batch)
		if sequentialDisplay {
			engine.SetBeforeToolCall(toolStream.ReplayCall)
		}
		logCall := func(tc db.ToolCall, output string, toolErr error) {
			a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
		}
		var halted, aborted bool
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
		timer.Time("tool", "end")
		a.emit(Event{Type: "turn_end", SessionID: sessionID, Message: assistantMsg})
		if halted || aborted {
			if ctx.Err() != nil {
				runErr = ctx.Err()
			} else {
				result.Status = RunBlocked
				runErr = fmt.Errorf("agent execution blocked")
			}
			return
		}
		lastToolTrailingNewlines = ncw.trailingNewlines
	}

	if loader != nil {
		loader.Stop()
	}
	result.Status = RunLimitReached
	runErr = fmt.Errorf("maximum reasoning steps reached (%d)", maxSteps)
	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	fmt.Fprintf(writerToUse, "\n%s reached maximum reasoning steps limit (%d).\n", errStyle.Render("warning:"), maxSteps)
	return
}

func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"context_length_exceeded", "maximum context length", "context window exceeded", "prompt is too long", "input token count exceeds"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
