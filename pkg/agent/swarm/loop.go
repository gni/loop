package swarm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/run"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (ma *MultiAgent) streamBufferView() io.Writer {
	return &streamBufferWriter{ma: ma}
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

func (ma *MultiAgent) recordTaskAndAgentState(taskID, taskStatus, agentStatus, response string, taskErr error) error {
	if ma.Manager == nil {
		return nil
	}
	if taskID != "" {
		ma.Manager.UpdateTaskStatus(taskID, taskStatus, response, taskErr)
	}
	if err := ma.Manager.SaveAgentState(ma, agentStatus); err != nil {
		return fmt.Errorf("persist subagent %q status %q: %w", ma.Name, agentStatus, err)
	}
	return nil
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
				startErr := ma.recordTaskAndAgentState(taskID, "running", "running", "", nil)

				writer := ma.resolveWriter(w)
				fmt.Fprintf(writer, "\n[%s] received task from %s: %s\n",
					nameStyle.Render(ma.Name),
					msg.Name,
					msg.Content,
				)

				turnCtx, turnCancel := context.WithCancel(ma.Context)
				ma.setActiveTurn(turnCtx, turnCancel, time.Now())

				var response db.Message
				err := startErr
				if err == nil {
					response, err = ma.executeLoop(turnCtx, writer, theme)
				}

				turnCancel()
				ma.setActiveTurn(nil, nil, time.Time{})

				if err != nil {
					if saveErr := ma.recordTaskAndAgentState(taskID, "failed", "failed", "", err); saveErr != nil {
						err = errors.Join(err, saveErr)
					}

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

				if saveErr := ma.recordTaskAndAgentState(taskID, "completed", "idle", response.Content, nil); saveErr != nil {
					if ma.Manager != nil && taskID != "" {
						ma.Manager.UpdateTaskStatus(taskID, "failed", "", saveErr)
					}
					response = db.Message{Role: "assistant", Name: ma.Name, Content: fmt.Sprintf("Error: %v", saveErr)}
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

func (ma *MultiAgent) executeLoop(ctx context.Context, w io.Writer, theme style.UITheme) (response db.Message, runErr error) {
	if ma.BaseAgent == nil {
		return db.Message{}, fmt.Errorf("subagent has no runtime")
	}
	ctx = run.WithPlan(ctx, ma.GetTodos)
	ctx = run.WithObjective(ctx, latestUserPrompt(newSwarmSink(ma).Messages()))
	ctx, runConfig, err := ma.BaseAgent.SnapshotRun(ctx)
	if err != nil {
		return db.Message{}, err
	}
	writer := ma.resolveWriter(w)
	rawW := agent.UnwrapWriter(writer)

	maxSteps := 30
	var guidedMode, allowApproval, runHooks bool
	if ma.BaseAgent != nil {
		guidedMode = ma.BaseAgent.IsGuidedMode()
		runHooks = true
		if runConfig != nil {
			if runConfig.MaxReasoningSteps > 0 {
				maxSteps = runConfig.MaxReasoningSteps
			}
			allowApproval = guidedMode
		}
	}
	if ma.Parent != nil {
		guidedMode = false
		allowApproval = false
	}

	engine := agent.NewTurnEngine(ma.BaseAgent, agent.EnginePolicy{
		AgentName:     ma.Name,
		Config:        runConfig,
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
	defer func() {
		if err := sink.Err(); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	renderedAgentName := style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name)

	allowlist := ma.GetToolAllowlist()
	var (
		ncw                   *agent.NewlineCounterWriter
		sr                    agent.StreamRenderer
		lastUserPrompt        string
		responseHeaderStarted bool
		generationDuration    time.Duration
	)
	outcome, err := run.Execute(ctx, run.Policy{MaxSteps: maxSteps, RecoveryAttempts: run.DefaultRecoveryAttempts}, run.Driver{
		Observe: ma.BaseAgent.RunObserver("subagent:" + ma.Name),
		Prepare: func(ctx context.Context, iter int) error {
			if err := sink.Err(); err != nil {
				return err
			}
			engine.BeginTurn()
			compacted, err := ma.compressIfNeeded(ctx, writer, theme)
			if err != nil {
				return err
			}
			if compacted {
				engine.HistoryCompacted()
			}
			if engine.ShouldRecap() {
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
			return sink.Err()
		},
		Infer: func(ctx context.Context, iter int) (*db.Message, error) {
			historyCopy := sink.Messages()
			if ma.BaseAgent != nil {
				toolsForLog := ma.BaseAgent.Registry.GetAvailableTools(allowlist)
				ma.BaseAgent.DebugLogLLMRequest("subagent:"+ma.Name, iter, runConfig.Model, runConfig.Endpoint, historyCopy, toolsForLog)
			}

			chunkChan := make(chan agent.StreamChunk, 100)
			type completionResult struct {
				msg      *db.Message
				duration time.Duration
				dropped  int64
				err      error
			}
			resChan := make(chan completionResult, 1)

			go func() {
				msg, dur, dropped, err := ma.BaseAgent.StreamChatCompletionsIsolated(ctx, historyCopy, allowlist, chunkChan)
				close(chunkChan)
				resChan <- completionResult{msg: msg, duration: dur, dropped: dropped, err: err}
			}()

			ma.StreamBufferMu.Lock()
			ma.StreamBuffer = new(bytes.Buffer)
			ma.StreamBufferMu.Unlock()
			teeW := agent.NewCustomTeeWriter(writer, ma.streamBufferView())

			lastUserPrompt = latestUserPrompt(historyCopy)
			ncw = agent.NewNewlineCounterWriter(teeW)
			enableThinking := ma.BaseAgent != nil && ma.BaseAgent.ShouldShowThinking()
			if ma.BaseAgent != nil {
				sr = ma.BaseAgent.NewTurnStreamRenderer(ncw, theme, ma.Name, lastUserPrompt)
			} else {
				sr = agent.NewFallbackStreamRenderer(ncw)
				sr.SetPrompt(lastUserPrompt)
			}

			echoFilter := agent.NewPromptEchoFilter(lastUserPrompt)

			responseHeaderStarted = false
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
				activeSelected := ma.Manager == nil || ma.Manager.CurrentActiveAgent() == ma || (ma.Parent == nil && ma.Manager.CurrentActiveAgent() == nil)
				if activeSelected && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
					ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, tokens, subagentCtxLimit, generating, tps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
					ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
				}
			}

			// Render subagent tool calls from their completed arguments at
			// execution time, using the same single-header path as the main agent.
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
				case "tool_name", "tool_target":
					// The engine prints tool titles after the model call completes.
				case "tool_call":
					subagentStreamChars += len(chunk.Content)
					subagentCompletionTokens = agent.TokensFromChars(subagentStreamChars)
				default:
					startResponseHeader()
				}

				if now := time.Now(); now.Sub(lastDraw) >= 100*time.Millisecond {
					updateSubagentStatus(true, calcStreamTPS(subagentCompletionTokens, subagentGenStart, now), subagentCompletionTokens)
					lastDraw = now
				}
			}

			completion := <-resChan

			if completion.dropped > 0 && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
				fmt.Fprintf(ncw, "\n[%s] output truncated: %d stream chunk(s) could not be delivered\n",
					style.NewStyle().Foreground(theme.Error).Bold(true).Render(ma.Name), completion.dropped)
			}

			if pending := echoFilter.Flush(); pending != "" && enableThinking && !responseHeaderStarted {
				startResponseHeader()
				sr.WriteReasoning(pending)
			}
			sr.Flush()

			assistantMsg := completion.msg
			if assistantMsg != nil && assistantMsg.CompletionTokens > 0 {
				subagentCompletionTokens = assistantMsg.CompletionTokens
			}

			if !subagentGenStart.IsZero() {
				updateSubagentStatus(false, calcStreamTPS(subagentCompletionTokens, subagentGenStart, time.Now()), subagentCompletionTokens)
			}

			ma.StreamBufferMu.Lock()
			ma.StreamBuffer = nil
			ma.StreamBufferMu.Unlock()

			generationDuration = completion.duration
			return completion.msg, completion.err
		},
		Record: func(ctx context.Context, iter int, assistantMsg *db.Message) error {
			if lastUserPrompt != "" && assistantMsg.ReasoningContent != "" {
				assistantMsg.ReasoningContent = agent.StripEchoedPrompt(assistantMsg.ReasoningContent, lastUserPrompt)
			}
			assistantMsg.ReasoningDuration = sr.GetReasoningDuration()

			if ma.BaseAgent != nil {
				ma.BaseAgent.DebugLogLLMResponse("subagent:"+ma.Name, iter, assistantMsg, generationDuration)
			}

			sink.Append(*assistantMsg)
			return sink.Err()
		},
		Complete: func(ctx context.Context, iter int, assistantMsg *db.Message) error {
			if !responseHeaderStarted {
				fmt.Fprintf(ncw, "\n[%s] response: %s\n", renderedAgentName, assistantMsg.Content)
			} else {
				fmt.Fprintln(ncw)
			}
			return nil
		},
		Execute: func(ctx context.Context, iter int, assistantMsg *db.Message) error {
			ncw.EnsureNewline()
			halted, aborted := engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, nil, ncw, theme, newSwarmRenderer(ma), func(tc db.ToolCall, output string, toolErr error) {
				ma.BaseAgent.DebugLogToolExecution("subagent:"+ma.Name, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
			})
			if err := sink.Err(); err != nil {
				return err
			}
			if halted || aborted {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return run.ErrBlocked
			}
			return nil
		},
		Compact: func(ctx context.Context) error {
			if err := ma.compactHistory(ctx, writer, theme); err != nil {
				return err
			}
			engine.HistoryCompacted()
			return nil
		},
		IsContextOverflow: agent.IsContextOverflow,
		IsRecoverable:     agent.IsRecoverableInferenceError,
		Interrupted: func(ctx context.Context, iter int, msg *db.Message, err error) error {
			if msg != nil && (msg.Content != "" || msg.ReasoningContent != "") {
				partial := *msg
				partial.ToolCalls = nil
				partial.StopReason = "interrupted"
				sink.Append(partial)
			}
			return sink.Err()
		},
	})
	if err != nil {
		return db.Message{}, err
	}
	if outcome.Message == nil {
		return db.Message{}, agent.ErrEmptyResponse
	}
	return *outcome.Message, nil
}
