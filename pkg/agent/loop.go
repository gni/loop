package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

func formatTurnElapsed(startTime time.Time, theme style.UITheme, showMicroseconds bool) string {
	now := time.Now()
	timeStr := fmt.Sprintf("%s (%s)", now.Format("2006-01-02 15:04:05"), style.FormatElapsedDuration(now.Sub(startTime), showMicroseconds))
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
	if err := a.Config.Nostop.Validate(); err != nil {
		result.Status = RunFailed
		result.Error = err.Error()
		return result, err
	}
	nostop := a.Config.Nostop.Enabled()
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
				fmt.Fprintln(writerToUse, formatTurnElapsed(startTime, theme, a.Config.ShowMicroseconds))
			} else {
				// Keep the elapsed-time text on the last occupied scroll row.
				// The reserved row beneath it is already the status-bar gap.
				fmt.Fprint(writerToUse, formatTurnElapsed(startTime, theme, a.Config.ShowMicroseconds))
			}
		}
	}()

	// The runtime owns a different history slice from the REPL until the
	// turn returns. Live redraws need this slice, including completed tool
	// steps from the current turn, plus the renderer's uncommitted inference.
	a.CurrentStreamMu.Lock()
	a.CurrentHistorySnapshot = func() []db.Message {
		a.MessagesMu.RLock()
		defer a.MessagesMu.RUnlock()
		return message.CloneAll(*messages)
	}
	a.CurrentStreamMu.Unlock()
	defer func() {
		a.CurrentStreamMu.Lock()
		a.CurrentHistorySnapshot = nil
		a.CurrentStreamMu.Unlock()
	}()

	a.appendTurnInput(messages, prompt, sessionID)

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

	failures := 0
	for iter := 1; nostop || iter <= maxSteps; iter++ {
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

		globalPromptTokensEst, effectiveLimit, err := a.prepareAgentStep(ctx, messages, allowlist, sessionID, iter, lastToolTrailingNewlines, engine, sink, timer, writerToUse, theme)
		if err != nil {
			if nostop && ctx.Err() == nil && a.persistenceError() == nil && recoverableNostopError(err) {
				failures++
				a.emit(Event{Type: "retry", SessionID: sessionID, Output: "nostop context recovery: " + err.Error()})
				if waitErr := a.pauseNostop(ctx, writerToUse, loader, sessionID, theme, failures); waitErr != nil {
					runErr = waitErr
					return
				}
				continue
			}
			runErr = err
			return
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

		streamBuf := new(bytes.Buffer)
		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = streamBuf
		a.CurrentStreamMu.Unlock()

		teeWriter := &CustomTeeWriter{screen: writerToUse, buffer: &lockedStreamBuffer{mu: &a.CurrentStreamMu, buf: streamBuf}}
		ncw := &NewlineCounterWriter{Writer: teeWriter}
		a.SetCurrentWriter(ncw)
		sr := a.NewTurnStreamRenderer(ncw, theme, "loop", prompt)
		if replay, ok := sr.(interface {
			ReplayActiveTurn(io.Writer, bool, style.UITheme)
		}); ok {
			a.CurrentStreamMu.Lock()
			a.CurrentStreamReplay = replay.ReplayActiveTurn
			a.CurrentStreamMu.Unlock()
		}

		echoFilter := NewPromptEchoFilter(prompt)
		priorCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		updateStreamStatus := func(generating bool) {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, effectiveLimit, generating, 0, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		updateStreamStatus(true)

		// Stream each tool call in index order while keeping interleaved
		// argument fragments from different files separate.
		toolStream := newToolStreamSequencer(sr, true)
		batch := NewToolBatch(func(update ToolLifecycleEvent) {
			if presenter, ok := sr.(interface{ ObserveToolLifecycle(ToolLifecycleEvent) }); ok {
				presenter.ObserveToolLifecycle(update)
			}
			a.emit(Event{Type: "tool_lifecycle", SessionID: sessionID, Data: update})
		})
		toolStream.SetBatch(batch)
		// Previews follow model order in both execution modes. Execution
		// results remain paired with their call even when workers finish
		// out of order. The execution policy is unchanged.
		sequentialDisplay := true
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
		if presenter, ok := sr.(interface {
			ShowLiveToolContent(int, string, string, string)
			EndLiveToolContent()
		}); ok {
			toolStream.PreviewOnly()
			toolStream.SetLiveContentCallback(func(index int, name, path, delta string) {
				// Ctrl+O may change this setting while the provider is streaming.
				// Apply it before every chunk, including the first of a new file.
				if view, ok := sr.(interface{ SetLiveCollapse(bool) }); ok {
					view.SetLiveCollapse(a.Config.CollapseResults)
				}
				if (name == "write" || name == "edit") && !a.Config.StreamWrites {
					delta = ""
				}
				presenter.ShowLiveToolContent(index, name, path, delta)
			})
			toolStream.SetEditDiffPreview(func(name, arguments string) string {
				return PreviewToolDiff(a, name, arguments)
			})
			toolStream.SetOnReady(func(index int, name, arguments string) {
				// Only commit a contiguous series of completed write/edit
				// calls. Interleaved, unfinished or unsupported calls continue
				// through the standard post-inference execution engine.
				if !a.Config.StreamWrites || earlyStopped || ctx.Err() != nil || index != len(earlyCalls) ||
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
		a.consumeTurnChunks(ctx, chunkChan, sessionID, loader, echoFilter, sr, toolStream)
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
		// Handle a toggle received with the last provider chunk before
		// the live stream snapshot is released.
		canReplay := true
		if view, ok := sr.(interface{ SafeToReplay() bool }); ok {
			canReplay = view.SafeToReplay()
		}
		if canReplay && a.OnStreamProgress != nil && a.OnStreamProgress() {
			if view, ok := sr.(interface{ InvalidateTrackedRows() }); ok {
				view.InvalidateTrackedRows()
			}
		}
		updateStreamStatus(false)

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = nil
		a.CurrentStreamReplay = nil
		a.CurrentStreamMu.Unlock()

		res := <-resChan
		// Retrying after a speculative file operation could replay a side effect.
		if res.err != nil && len(earlyCalls) == 0 && ctx.Err() == nil && isContextOverflow(res.err) && len(*messages) > 4 {
			a.emit(Event{Type: "retry", SessionID: sessionID, Output: "compacting context after provider context rejection"})
			if err := a.CompressHistory(ctx, messages, sessionID, theme, writerToUse); err == nil {
				retryChunks := make(chan StreamChunk, 200)
				retryDone := make(chan streamResult, 1)
				go func() {
					msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, retryChunks)
					close(retryChunks)
					retryDone <- streamResult{msg, err}
				}()
				for chunk := range relayStreamChunks(ctx, retryChunks) {
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
		if res.err == nil && !hasUsableAssistantResponse(res.msg) {
			res.err = ErrEmptyResponse
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
			if nostop && ctx.Err() == nil && recoverableNostopError(res.err) {
				if err := a.persistenceError(); err != nil {
					runErr = err
					return
				}
				failures++
				a.emit(Event{Type: "retry", SessionID: sessionID, Output: "nostop recovery: " + res.err.Error()})
				ncw.EnsureNewline()
				if err := a.pauseNostop(ctx, writerToUse, loader, sessionID, theme, failures); err != nil {
					runErr = err
					return
				}
				runErr = nil
				lastToolTrailingNewlines = ncw.trailingNewlines
				continue
			}
			if loader != nil {
				loader.Stop()
			}
			a.renderTurnError(writerToUse, ncw, sink, res.err, ctx.Err(), isNonInteractive, theme)
			return
		}

		assistantMsg := res.msg
		failures = 0

		if prompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, prompt)
		}

		if !reconcileEarlyToolCalls(assistantMsg, earlyCalls, earlyMessages) {
			batch.CancelUnfinished()
			sink.Append(db.Message{Role: "assistant", ToolCalls: earlyCalls, StopReason: "interrupted"})
			for _, m := range earlyMessages {
				sink.Append(m)
			}
			runErr = fmt.Errorf("provider changed a tool call after its completed arguments were executed; refusing remaining calls")
			a.renderTurnError(writerToUse, ncw, sink, runErr, ctx.Err(), isNonInteractive, theme)
			return
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
			result.Text = assistantMsg.Content
			// A usable final reply ends the objective in every pacing mode.
			// Nonstop extends tool execution and recovery, not completed work.
			if nostop {
				ncw.EnsureNewline()
			}
			timePrinted = true
			a.renderFinalAgentTurn(assistantMsg, sr, loader, rawW, writerToUse, sessionID, startTime, theme)
			syncPostTurnStatus()
			return
		}

		syncPostTurnStatus()

		timer.Time("tool", "start")
		halted, aborted := a.runRemainingToolCalls(ctx, engine, batch, toolStream, assistantMsg, earlyCalls, sequentialDisplay, sink, sr, loader, ncw, theme, sessionID, iter)
		timer.Time("tool", "end")
		a.emit(Event{Type: "turn_end", SessionID: sessionID, Message: assistantMsg})
		// Tool execution may run without provider chunks. Handle queued Ctrl+O
		// after the tool results have entered saved conversation history.
		canReplay = true
		if view, ok := sr.(interface{ SafeToReplay() bool }); ok {
			canReplay = view.SafeToReplay()
		}
		if canReplay && a.OnStreamProgress != nil && a.OnStreamProgress() {
			if view, ok := sr.(interface{ InvalidateTrackedRows() }); ok {
				view.InvalidateTrackedRows()
			}
		}
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
		if nostop && a.Config.Nostop.PauseAfter(iter) {
			if err := a.pauseNostop(ctx, writerToUse, loader, sessionID, theme, 0); err != nil {
				runErr = err
				return
			}
		}
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
