package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (ma *MultiAgent) streamBufferView() io.Writer {
	return &streamBufferWriter{ma: ma}
}

func (ma *MultiAgent) streamBufferBytes() []byte {
	ma.StreamBufferMu.Lock()
	defer ma.StreamBufferMu.Unlock()
	if ma.StreamBuffer == nil {
		return nil
	}
	return append([]byte(nil), ma.StreamBuffer.Bytes()...)
}

func (ma *MultiAgent) resolveWriter(fallback io.Writer) io.Writer {
	if ma != nil && ma.BaseAgent != nil {
		if w := ma.BaseAgent.CurrentWriter(); w != nil {
			return w
		}
	}
	return fallback
}

func (ma *MultiAgent) setActiveTurn(ctx context.Context, cancel context.CancelFunc, started time.Time) {
	ma.HistoryMu.Lock()
	ma.ActiveContext = ctx
	ma.ActiveCancel = cancel
	ma.ActiveStarted = started
	ma.HistoryMu.Unlock()
}

type streamBufferWriter struct{ ma *MultiAgent }

func (s *streamBufferWriter) Write(p []byte) (int, error) {
	s.ma.StreamBufferMu.Lock()
	defer s.ma.StreamBufferMu.Unlock()
	if s.ma.StreamBuffer == nil {
		return len(p), nil
	}
	return s.ma.StreamBuffer.Write(p)
}

func calcStreamTPS(tokens int, start time.Time, now time.Time) float64 {
	if start.IsZero() || tokens <= 0 {
		return 0
	}
	if elapsed := now.Sub(start).Seconds(); elapsed > 0 {
		return float64(tokens) / elapsed
	}
	return 0
}

func latestUserPrompt(history []db.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			return history[i].Content
		}
	}
	return ""
}

func (ma *MultiAgent) recordTaskAndAgentState(taskID, taskStatus, agentStatus, response string, err error) {
	if ma.Manager == nil {
		return
	}
	if taskID != "" {
		ma.Manager.UpdateTaskStatus(taskID, taskStatus, response, err)
	}
	_ = ma.Manager.SaveAgentState(ma, agentStatus)
}

func (ma *MultiAgent) Start(w io.Writer, theme style.UITheme) {
	go func() {
		nameStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
		errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)

		for {
			select {
			case <-ma.Context.Done():
				return
			case msg, ok := <-ma.Input:
				if !ok {
					return
				}

				ma.HistoryMu.Lock()
				ma.History = append(ma.History, msg)
				ma.HistoryMu.Unlock()

				if msg.Role != "user" {
					continue
				}
				taskID := msg.ToolCallID
				ma.recordTaskAndAgentState(taskID, "running", "running", "", nil)

				writer := ma.resolveWriter(w)
				fmt.Fprintf(writer, "\n[%s] received task from %s: %s\n",
					nameStyle.Render(ma.Name),
					msg.Name,
					msg.Content,
				)

				turnCtx, turnCancel := context.WithCancel(ma.Context)
				ma.setActiveTurn(turnCtx, turnCancel, time.Now())

				response, err := ma.executeLoop(turnCtx, writer, theme)

				turnCancel()
				ma.setActiveTurn(nil, nil, time.Time{})

				if err != nil {
					ma.recordTaskAndAgentState(taskID, "failed", "failed", "", err)

					if errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, context.Canceled) {
						label := "error"
						if errors.Is(err, context.DeadlineExceeded) {
							label = "timed out"
						}
						fmt.Fprintf(writer, "\n%s [%s] %s: %v\n",
							errStyle.Render("!"),
							nameStyle.Render(ma.Name),
							label,
							err,
						)
					}

					select {
					case ma.Output <- db.Message{Role: "assistant", Name: ma.Name, Content: fmt.Sprintf("Error: %v", err)}:
					case <-ma.Context.Done():
						return
					}
					continue
				}

				ma.recordTaskAndAgentState(taskID, "completed", "idle", response.Content, nil)

				select {
				case ma.Output <- response:
				case <-ma.Context.Done():
					return
				}
			}
		}
	}()
}

func (ma *MultiAgent) executeLoop(ctx context.Context, w io.Writer, theme style.UITheme) (db.Message, error) {
	writer := ma.resolveWriter(w)
	rawW := agent.UnwrapWriter(writer)

	maxSteps := 30
	var guidedMode, allowApproval, runHooks bool
	if ma.BaseAgent != nil {
		guidedMode = ma.BaseAgent.IsGuidedMode()
		runHooks = true
		if ma.BaseAgent.Config != nil {
			if ma.BaseAgent.Config.MaxReasoningSteps > 0 {
				maxSteps = ma.BaseAgent.Config.MaxReasoningSteps
			}
			allowApproval = guidedMode
		}
	}

	engine := agent.NewTurnEngine(ma.BaseAgent, agent.EnginePolicy{
		AgentName:     ma.Name,
		ToolAllowlist: ma.GetToolAllowlist,
		AllowApproval: allowApproval,
		RunHooks:      runHooks,
		GuidedMode:    guidedMode,
		SessionKey:    "subagent:" + ma.Name,
		ShowRecapLine: ma.Parent == nil,
		ExecContext: func() tool.AgentContext {
			return &multiAgentContext{AgentContext: ma.BaseAgent, ma: ma}
		},
	})
	sink := newSwarmSink(ma)
	renderedAgentName := style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name)

	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return db.Message{}, ctx.Err()
		}
		engine.BeginTurn()

		ma.compressIfNeeded(ctx, writer, theme)

		allowlist := ma.GetToolAllowlist()
		if engine.ShouldRecap() && ma.BaseAgent != nil {
			msgs := sink.Messages()
			promptTokens, _ := ma.BaseAgent.GetGlobalTokens(msgs, allowlist)
			totalCompletion := ma.BaseAgent.GetSessionTotalCompletionTokens(msgs)
			effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(promptTokens)
			engine.MaybeRecap(sink, promptTokens, totalCompletion, effectiveLimit, writer, theme)
		}

		currentPrompt := ma.GetSystemPrompt()
		ma.HistoryMu.Lock()
		if len(ma.History) > 0 && ma.History[0].Role == "system" {
			ma.History[0].Content = currentPrompt
		}
		ma.HistoryMu.Unlock()

		historyCopy := sink.Messages()
		if ma.BaseAgent != nil {
			toolsForLog := ma.BaseAgent.Registry.GetAvailableTools(allowlist)
			ma.BaseAgent.DebugLogLLMRequest("subagent:"+ma.Name, iter, ma.BaseAgent.Config.Model, ma.BaseAgent.Config.Endpoint, historyCopy, toolsForLog)
		}

		chunkChan := make(chan agent.StreamChunk, 100)
		errChan := make(chan error, 1)
		dropsBefore := ma.BaseAgent.DroppedChunks()

		var assistantMsg *db.Message
		go func() {
			msg, err := ma.BaseAgent.StreamChatCompletions(ctx, historyCopy, allowlist, chunkChan)
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
			errChan <- err
		}()

		ma.StreamBufferMu.Lock()
		ma.StreamBuffer = new(bytes.Buffer)
		ma.StreamBufferMu.Unlock()
		teeW := agent.NewCustomTeeWriter(writer, ma.streamBufferView())

		lastUserPrompt := latestUserPrompt(historyCopy)
		ncw := agent.NewNewlineCounterWriter(teeW)
		enableThinking := ma.BaseAgent != nil && ma.BaseAgent.ShouldShowThinking()
		var sr agent.StreamRenderer
		if ma.BaseAgent != nil {
			sr = ma.BaseAgent.NewTurnStreamRenderer(ncw, theme, ma.Name, lastUserPrompt)
		} else {
			sr = agent.NewFallbackStreamRenderer(ncw)
			sr.SetPrompt(lastUserPrompt)
		}

		echoFilter := agent.NewPromptEchoFilter(lastUserPrompt)

		var responseHeaderStarted bool
		var subagentStreamChars, subagentCompletionTokens int
		var subagentGenStart, lastDraw time.Time
		subagentCtxLimit := 128000
		if ma.BaseAgent != nil {
			estimatedPrompt, _ := ma.BaseAgent.GetGlobalTokens(historyCopy, allowlist)
			subagentCtxLimit = ma.BaseAgent.GetEffectiveContextLimit(estimatedPrompt)
		}

		startResponseHeader := func() {
			if !responseHeaderStarted {
				fmt.Fprintf(ncw, "\n[%s] response: ", renderedAgentName)
				responseHeaderStarted = true
			}
		}

		updateSubagentStatus := func(generating bool, tps float64, tokens int) {
			if ma.Parent == nil && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
				ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, tokens, subagentCtxLimit, generating, tps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
				ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
			}
		}

		// The first read is rendered by the executor after the LLM stream
		// finishes, keeping the subagent display in execution order.
		deferredFirstRead := false
		for chunk := range chunkChan {
			if subagentGenStart.IsZero() {
				subagentGenStart = time.Now()
				lastDraw = subagentGenStart
			}

			switch chunk.Type {
			case "reasoning":
				subagentStreamChars += len(chunk.Content)
				subagentCompletionTokens = agent.TokensFromChars(subagentStreamChars)
				if enableThinking {
					if cleaned := echoFilter.Write(chunk.Content); cleaned != "" {
						startResponseHeader()
						sr.WriteReasoning(cleaned)
					}
				}
			case "text":
				subagentStreamChars += len(chunk.Content)
				subagentCompletionTokens = agent.TokensFromChars(subagentStreamChars)
				startResponseHeader()
				sr.Write(chunk.Content)
			case "tool_name":
				startResponseHeader()
				if chunk.ToolCallIndex == 0 {
					deferredFirstRead = chunk.Content == "read"
					if !deferredFirstRead {
						sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
					}
				}
			case "tool_target":
				startResponseHeader()
				if chunk.ToolCallIndex == 0 && !deferredFirstRead {
					sr.SetToolTarget(chunk.Content, chunk.ToolCallIndex)
				}
			case "tool_call":
				subagentStreamChars += len(chunk.Content)
				subagentCompletionTokens = agent.TokensFromChars(subagentStreamChars)
				startResponseHeader()
				if chunk.ToolCallIndex == 0 && !deferredFirstRead {
					sr.WriteToolCall(chunk.Content)
				}
			default:
				startResponseHeader()
			}

			if now := time.Now(); now.Sub(lastDraw) >= 100*time.Millisecond {
				updateSubagentStatus(true, calcStreamTPS(subagentCompletionTokens, subagentGenStart, now), subagentCompletionTokens)
				lastDraw = now
			}
		}

		if dropped := ma.BaseAgent.DroppedChunks() - dropsBefore; dropped > 0 && ma.BaseAgent.UI != nil {
			fmt.Fprintf(ncw, "\n[%s] output truncated: %d stream chunk(s) could not be delivered\n",
				style.NewStyle().Foreground(theme.Error).Bold(true).Render(ma.Name), dropped)
		}

		if pending := echoFilter.Flush(); pending != "" && enableThinking && !responseHeaderStarted {
			startResponseHeader()
			sr.WriteReasoning(pending)
		}
		sr.Flush()

		if assistantMsg != nil && assistantMsg.CompletionTokens > 0 {
			subagentCompletionTokens = assistantMsg.CompletionTokens
		}

		if !subagentGenStart.IsZero() {
			updateSubagentStatus(false, calcStreamTPS(subagentCompletionTokens, subagentGenStart, time.Now()), subagentCompletionTokens)
		}

		ma.StreamBufferMu.Lock()
		ma.StreamBuffer = nil
		ma.StreamBufferMu.Unlock()

		if err := <-errChan; err != nil {
			return db.Message{}, err
		}
		if assistantMsg == nil {
			return db.Message{}, fmt.Errorf("received empty completion response")
		}

		if lastUserPrompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = agent.StripEchoedPrompt(assistantMsg.ReasoningContent, lastUserPrompt)
		}
		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()

		sink.Append(*assistantMsg)

		if ma.BaseAgent != nil {
			ma.BaseAgent.DebugLogLLMResponse("subagent:"+ma.Name, iter, assistantMsg, ma.BaseAgent.LastGenerationDuration())
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if !responseHeaderStarted {
				fmt.Fprintf(ncw, "\n[%s] response: %s\n", renderedAgentName, assistantMsg.Content)
			} else {
				fmt.Fprintln(ncw)
			}
			return *assistantMsg, nil
		}

		halted, aborted := engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, nil, ncw, theme, newSwarmRenderer(ma, theme), func(tc db.ToolCall, output string, toolErr error) {
			if ma.BaseAgent != nil {
				ma.BaseAgent.DebugLogToolExecution("subagent:"+ma.Name, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
			}
		})
		if halted || aborted {
			haltMsg := engine.HaltNotice()
			if last := sink.Messages(); len(last) > 0 && last[len(last)-1].Role == "assistant" {
				haltMsg = last[len(last)-1].Content
			}
			if ma.Manager != nil && halted {
				_ = ma.Manager.SaveAgentState(ma, "failed")
			}
			return db.Message{Role: "assistant", Content: haltMsg}, nil
		}
	}

	return db.Message{}, fmt.Errorf("reached maximum reasoning steps limit (%d)", maxSteps)
}
