package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	return ma.StreamBuffer.Bytes()
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

func (ma *MultiAgent) Start(w io.Writer, theme style.UITheme) {
	go func() {
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

				taskID := msg.ToolCallID
				if ma.Manager != nil {
					if taskID != "" {
						ma.Manager.UpdateTaskStatus(taskID, "running", "", nil)
					}
					_ = ma.Manager.SaveAgentState(ma, "running")
				}

				if msg.Role != "user" {
					continue
				}

				writer := w
				if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter() != nil {
					writer = ma.BaseAgent.CurrentWriter()
				}
				fmt.Fprintf(writer, "\n[%s] received task from %s: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
					msg.Name,
					msg.Content,
				)

				turnCtx, turnCancel := context.WithCancel(ma.Context)
				ma.HistoryMu.Lock()
				ma.ActiveContext = turnCtx
				ma.ActiveCancel = turnCancel
				ma.ActiveStarted = time.Now()
				ma.HistoryMu.Unlock()

				response, err := ma.executeLoop(turnCtx, writer, theme)

				turnCancel()
				ma.HistoryMu.Lock()
				ma.ActiveContext = nil
				ma.ActiveCancel = nil
				ma.ActiveStarted = time.Time{}
				ma.HistoryMu.Unlock()

				if err != nil {
					if ma.Manager != nil {
						if taskID != "" {
							ma.Manager.UpdateTaskStatus(taskID, "failed", "", err)
						}
						_ = ma.Manager.SaveAgentState(ma, "failed")
					}

					if errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, context.Canceled) {
						label := "error"
						if errors.Is(err, context.DeadlineExceeded) {
							label = "timed out"
						}
						errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
						fmt.Fprintf(writer, "\n%s [%s] %s: %v\n",
							errStyle.Render("!"),
							style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
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

				if ma.Manager != nil {
					if taskID != "" {
						ma.Manager.UpdateTaskStatus(taskID, "completed", response.Content, nil)
					}
					_ = ma.Manager.SaveAgentState(ma, "idle")
				}

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
	writer := w
	if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter() != nil {
		writer = ma.BaseAgent.CurrentWriter()
	}
	rawW := agent.UnwrapWriter(writer)

	maxSteps := 30
	var guidedMode, allowApproval, runHooks bool
	if ma.BaseAgent != nil {
		guidedMode = ma.BaseAgent.IsGuidedMode()
		if ma.BaseAgent.Config != nil {
			if ma.BaseAgent.Config.MaxReasoningSteps > 0 {
				maxSteps = ma.BaseAgent.Config.MaxReasoningSteps
			}
			allowApproval = guidedMode
			runHooks = ma.BaseAgent.Config.BeforeToolHook != "" || ma.BaseAgent.Config.AfterToolHook != ""
		}
	}

	engine := agent.NewTurnEngine(ma.BaseAgent, agent.EnginePolicy{
		AgentName:     ma.Name,
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

	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return db.Message{}, ctx.Err()
		}
		engine.BeginTurn()

		if engine.ShouldRecap() {
			promptTokens, totalCompletion := agent.CalculateHistoryTokens(sink.Messages())
			effectiveLimit := ma.BaseAgent.GetEffectiveContextLimit(promptTokens)
			engine.MaybeRecap(sink, promptTokens, totalCompletion, effectiveLimit, writer, theme)
		}

		ma.compressIfNeeded(ctx, writer, theme)

		ma.HistoryMu.RLock()
		historyCopy := make([]db.Message, len(ma.History))
		copy(historyCopy, ma.History)
		ma.HistoryMu.RUnlock()

		allowlist := ma.GetToolAllowlist()
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

		var lastUserPrompt string
		for i := len(historyCopy) - 1; i >= 0; i-- {
			if historyCopy[i].Role == "user" {
				lastUserPrompt = historyCopy[i].Content
				break
			}
		}

		ncw := agent.NewNewlineCounterWriter(teeW)
		enableThinking := false
		if ma.BaseAgent != nil && ma.BaseAgent.Config != nil {
			effort := strings.ToLower(strings.TrimSpace(ma.BaseAgent.Config.ReasoningEffort))
			enableThinking = ma.BaseAgent.Config.ShowThinking && effort != "off" && effort != "none"
		}
		var sr agent.StreamRenderer
		if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
			sr = ma.BaseAgent.UI.NewStreamRenderer(ncw, theme, enableThinking, ma.BaseAgent.Config.StreamWrites, ma.Name)
		} else {
			sr = agent.NewFallbackStreamRenderer(ncw)
		}
		sr.SetPrompt(lastUserPrompt)

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
				fmt.Fprintf(ncw, "\n[%s] response: ", style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name))
				responseHeaderStarted = true
			}
		}

		updateSubagentStatus := func(generating bool, tps float64, tokens int) {
			if ma.Parent == nil && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
				ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, tokens, subagentCtxLimit, generating, tps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
				ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
			}
		}

		for chunk := range chunkChan {
			if subagentGenStart.IsZero() {
				subagentGenStart = time.Now()
				lastDraw = subagentGenStart
			}

			if chunk.Type == "reasoning" || chunk.Type == "text" || chunk.Type == "tool_call" {
				subagentStreamChars += len(chunk.Content)
				subagentCompletionTokens = agent.TokensFromChars(subagentStreamChars)
			}

			if chunk.Type == "reasoning" {
				if enableThinking {
					if cleaned := echoFilter.Write(chunk.Content); cleaned != "" {
						startResponseHeader()
						sr.WriteReasoning(cleaned)
					}
				}
			} else {
				startResponseHeader()
				switch chunk.Type {
				case "text":
					sr.Write(chunk.Content)
				case "tool_name":
					if chunk.ToolCallIndex == 0 {
						sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
					}
				case "tool_target":
					if chunk.ToolCallIndex == 0 {
						sr.SetToolTarget(chunk.Content, chunk.ToolCallIndex)
					}
				case "tool_call":
					if chunk.ToolCallIndex == 0 {
						sr.WriteToolCall(chunk.Content)
					}
				}
			}

			if now := time.Now(); now.Sub(lastDraw) >= 100*time.Millisecond {
				var tps float64
				if elapsed := now.Sub(subagentGenStart).Seconds(); elapsed > 0 {
					tps = float64(subagentCompletionTokens) / elapsed
				}
				updateSubagentStatus(true, tps, subagentCompletionTokens)
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
			var finalTps float64
			if elapsed := time.Since(subagentGenStart).Seconds(); elapsed > 0 {
				finalTps = float64(subagentCompletionTokens) / elapsed
			}
			updateSubagentStatus(false, finalTps, subagentCompletionTokens)
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

		ma.HistoryMu.Lock()
		ma.History = append(ma.History, *assistantMsg)
		ma.HistoryMu.Unlock()

		if ma.BaseAgent != nil {
			ma.BaseAgent.DebugLogLLMResponse("subagent:"+ma.Name, iter, assistantMsg, ma.BaseAgent.LastGenerationDuration())
		}
		if ma.Manager != nil {
			_ = ma.Manager.SaveAgentState(ma, "running")
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if !responseHeaderStarted {
				fmt.Fprintf(ncw, "\n[%s] response: %s\n",
					style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
					assistantMsg.Content,
				)
			} else {
				fmt.Fprintln(ncw)
			}
			return *assistantMsg, nil
		}

		if ma.BaseAgent != nil && ma.BaseAgent.Config != nil && !ma.BaseAgent.Config.ParallelToolCalls && len(assistantMsg.ToolCalls) > 1 {
			assistantMsg.ToolCalls = assistantMsg.ToolCalls[:1]
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