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
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// streamBufferView returns an io.Writer appending to this agent's own StreamBuffer
// under StreamBufferMu, so a concurrent redraw can read it safely while another agent's
// loop writes its own buffer.
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

// Start initiates the message processing loop for the subagent in a background goroutine.
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
				if taskID != "" && ma.Manager != nil {
					ma.Manager.UpdateTaskStatus(taskID, "running", "", nil)
					_ = ma.Manager.SaveAgentState(ma, "running")
				} else if ma.Manager != nil {
					_ = ma.Manager.SaveAgentState(ma, "running")
				}

				if msg.Role == "user" {
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
						if taskID != "" && ma.Manager != nil {
							ma.Manager.UpdateTaskStatus(taskID, "failed", "", err)
						}
						if ma.Manager != nil {
							_ = ma.Manager.SaveAgentState(ma, "failed")
						}

						// errors.Is so wrapped context.Canceled is still treated as a
						// cancellation; wrapped DeadlineExceeded (provider timeouts) stays
						// a real error and is printed as a timeout.
						if errors.Is(err, context.DeadlineExceeded) {
							errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
							fmt.Fprintf(writer, "\n%s [%s] timed out: %v\n",
								errStyle.Render("!"),
								style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
								err,
							)
						} else if !errors.Is(err, context.Canceled) {
							errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
							fmt.Fprintf(writer, "\n%s [%s] error: %v\n",
								errStyle.Render("!"),
								style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
								err,
							)
						}
						errMsg := db.Message{
							Role:    "assistant",
							Name:    ma.Name,
							Content: fmt.Sprintf("Error: %v", err),
						}
						select {
						case ma.Output <- errMsg:
						case <-ma.Context.Done():
							return
						}
						continue
					}

					if taskID != "" && ma.Manager != nil {
						ma.Manager.UpdateTaskStatus(taskID, "completed", response.Content, nil)
					}
					if ma.Manager != nil {
						_ = ma.Manager.SaveAgentState(ma, "idle")
					}

					select {
					case ma.Output <- response:
					case <-ma.Context.Done():
						return
					}
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

	maxSteps := ma.BaseAgent.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}

	guard := agent.NewTurnExecutionGuard()
	consecutiveGuardRejections := 0
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return db.Message{}, ctx.Err()
		}
		ma.compressIfNeeded(ctx, writer, theme)

		ma.HistoryMu.RLock()
		historyCopy := make([]db.Message, len(ma.History))
		copy(historyCopy, ma.History)
		ma.HistoryMu.RUnlock()

		if ma.BaseAgent != nil {
			toolsForLog := ma.BaseAgent.Registry.GetAvailableTools(ma.GetToolAllowlist())
			ma.BaseAgent.DebugLogLLMRequest("subagent:"+ma.Name, iter, ma.BaseAgent.Config.Model, ma.BaseAgent.Config.Endpoint, historyCopy, toolsForLog)
		}

		chunkChan := make(chan agent.StreamChunk, 100)
		errChan := make(chan error, 1)
		dropsBefore := ma.BaseAgent.DroppedChunks()

		var assistantMsg *db.Message
		go func() {
			allowlist := ma.GetToolAllowlist()
			msg, err := ma.BaseAgent.StreamChatCompletions(ctx, historyCopy, allowlist, chunkChan)
			// Same ordering as pkg/agent/loop.go: the message must be assigned before
			// the error is signalled, otherwise the reader can observe a nil message
			// for a successful turn.
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
			errChan <- err
		}()

		// Tee into this agent's own buffer, never BaseAgent.CurrentStreamBuffer: that
		// buffer belongs to the main loop and is read by pkg/ui/redraw.go, so sharing it
		// let concurrent subagents overwrite each other's (and the main loop's) output.
		ma.StreamBufferMu.Lock()
		ma.StreamBuffer = new(bytes.Buffer)
		ma.StreamBufferMu.Unlock()
		teeWriter := agent.NewCustomTeeWriter(writer, ma.streamBufferView())
		writer = teeWriter
		var lastUserPrompt string
		for i := len(historyCopy) - 1; i >= 0; i-- {
			if historyCopy[i].Role == "user" {
				lastUserPrompt = historyCopy[i].Content
				break
			}
		}

		ncw := agent.NewNewlineCounterWriter(writer)
		var sr agent.StreamRenderer
		enableThinking := false
		if ma.BaseAgent != nil && ma.BaseAgent.Config != nil {
			effort := strings.ToLower(strings.TrimSpace(ma.BaseAgent.Config.ReasoningEffort))
			enableThinking = ma.BaseAgent.Config.ShowThinking && effort != "off" && effort != "none"
		}
		if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
			sr = ma.BaseAgent.UI.NewStreamRenderer(ncw, theme, enableThinking, ma.BaseAgent.Config.StreamWrites, ma.Name)
		} else {
			sr = agent.NewFallbackStreamRenderer(ncw)
		}
		sr.SetPrompt(lastUserPrompt)

		// Same echo suppression as the main loop, so subagent output does not begin
		// with the model's quoted repeat of the delegated task.
		echoFilter := agent.NewPromptEchoFilter(lastUserPrompt)

		var responseHeaderStarted bool
		var subagentCompletionTokens int
		var subagentGenStart time.Time
		var lastDraw time.Time
		subagentCtxLimit := 128000
		if ma.BaseAgent != nil {
			subagentCtxLimit = ma.BaseAgent.GetEffectiveContextLimit(0)
		}

		for chunk := range chunkChan {
			if subagentGenStart.IsZero() {
				subagentGenStart = time.Now()
				lastDraw = subagentGenStart
			}

			if chunk.Type == "reasoning" || chunk.Type == "text" {
				subagentCompletionTokens++
			}

			if chunk.Type == "reasoning" {
				if enableThinking {
					cleaned := echoFilter.Write(chunk.Content)
					if cleaned == "" {
						continue
					}
					if !responseHeaderStarted {
						fmt.Fprintf(ncw, "\n[%s] response: ",
							style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
						)
						responseHeaderStarted = true
					}
					sr.WriteReasoning(cleaned)
				}
			} else {
				if !responseHeaderStarted {
					fmt.Fprintf(ncw, "\n[%s] response: ",
						style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
					)
					responseHeaderStarted = true
				}

				if chunk.Type == "text" {
					sr.Write(chunk.Content)
				} else if chunk.Type == "tool_name" {
					if chunk.ToolCallIndex == 0 {
						sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
					}
				} else if chunk.Type == "tool_call" {
					if chunk.ToolCallIndex == 0 {
						sr.WriteToolCall(chunk.Content)
					}
				}
			}

			now := time.Now()
			if ma.Parent == nil && now.Sub(lastDraw) >= 100*time.Millisecond && ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
				elapsed := now.Sub(subagentGenStart).Seconds()
				var tps float64
				if elapsed > 0 {
					tps = float64(subagentCompletionTokens) / elapsed
				}

				ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, subagentCompletionTokens, subagentCtxLimit, true, tps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
				ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
				lastDraw = now
			}
		}
		if dropped := ma.BaseAgent.DroppedChunks() - dropsBefore; dropped > 0 && ma.BaseAgent.UI != nil {
			fmt.Fprintf(ncw, "\n[%s] output truncated: %d stream chunk(s) could not be delivered\n",
				style.NewStyle().Foreground(theme.Error).Bold(true).Render(ma.Name), dropped)
		}

		if pending := echoFilter.Flush(); pending != "" && enableThinking && !responseHeaderStarted {
			fmt.Fprintf(ncw, "\n[%s] response: ",
				style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(ma.Name),
			)
			responseHeaderStarted = true
			sr.WriteReasoning(pending)
		}
		sr.Flush()

		if ma.Parent == nil && ma.BaseAgent != nil && !subagentGenStart.IsZero() {
			elapsed := time.Since(subagentGenStart).Seconds()
			var finalTps float64
			if elapsed > 0 {
				finalTps = float64(subagentCompletionTokens) / elapsed
			}

			ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, subagentCompletionTokens, subagentCtxLimit, false, finalTps, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
			ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
		}

		ma.StreamBufferMu.Lock()
		ma.StreamBuffer = nil
		ma.StreamBufferMu.Unlock()

		err := <-errChan
		if err != nil {
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

		if ma.Parent == nil && ma.BaseAgent != nil && ma.BaseAgent.UI != nil && assistantMsg.CompletionTokens > 0 {
			ma.BaseAgent.UI.UpdateStatus(ma.BaseAgent.Config.Model, -1, -1, assistantMsg.CompletionTokens, subagentCtxLimit, false, 0, ma.BaseAgent.CountActiveTasks(), ma.BaseAgent.Config.ShowTokens)
			ma.BaseAgent.UI.DrawStatusBar(rawW, theme)
		}
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

		for idx, tc := range assistantMsg.ToolCalls {
			if ctx.Err() != nil {
				return db.Message{}, ctx.Err()
			}

			isSubagent := strings.HasPrefix(tc.Function.Name, "subagent__")
			wasStreamed := sr.GetToolTitleLineNumber(idx) != -1

			if !wasStreamed {
				prefixStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
				fmt.Fprintf(ncw, "%s [%s] calling tool:\n",
					style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("❖"),
					prefixStyle.Render(ma.Name),
				)
				if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
					ma.BaseAgent.UI.RenderToolHeader(ncw, theme, tc.Function.Name, tc.Function.Arguments)
				} else {
					fmt.Fprintf(ncw, "› %s\n", tc.Function.Name)
				}
			}

			var output string
			var toolErr error

			if guardErr := guard.CheckPreExecution(tc.Function.Name, tc.Function.Arguments); guardErr != nil {
				toolErr = guardErr
				output = guardErr.Error()
				consecutiveGuardRejections++
				if ma.BaseAgent != nil {
					ma.BaseAgent.DebugLogRepetition("subagent:"+ma.Name, tc.Function.Name, tc.Function.Arguments, guard.ConsecutiveIdenticalCount(), guardErr.Error())
				}
			} else {
				consecutiveGuardRejections = 0
				mac := &multiAgentContext{
					AgentContext: ma.BaseAgent,
					ma:           ma,
				}
				startTool := time.Now()
				output, toolErr = ma.BaseAgent.Registry.Execute(mac, tc.Function.Name, tc.Function.Arguments)
				toolDuration := time.Since(startTool)
				if ma.BaseAgent != nil {
					ma.BaseAgent.DebugLogToolExecution("subagent:"+ma.Name, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, toolDuration)
				}
			}

			guard.RecordPostExecution(tc.Function.Name, tc.Function.Arguments, output, toolErr)
			if reminder := guard.GetAdvisoryReminder(tc.Function.Name, tc.Function.Arguments); reminder != "" {
				output = output + "\n\n[" + reminder + "]"
			}

			if toolErr != nil {
				output = agent.FormatToolExecutionFailure(tc.Function.Name, output, toolErr)
			}
			if output == "" {
				output = "(no output)"
			}

			// Same deterministic pruning and spill-to-disk the main loop applies.
			output = ma.pruneToolOutput(tc.ID, tc.Function.Name, output)

			if !isSubagent {
				sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
			}

			if !isSubagent {
				bodyStreamed := sr.DidStreamToolBody(idx) || (ma.BaseAgent != nil && ma.BaseAgent.DidStreamLiveBody())
				if ma.BaseAgent != nil && ma.BaseAgent.UI != nil {
					ma.BaseAgent.UI.RenderToolOutput(ncw, output, toolErr != nil, ma.BaseAgent.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
				} else {
					if !bodyStreamed {
						fmt.Fprintln(ncw, output)
					}
				}
			}

			ma.HistoryMu.Lock()
			ma.History = append(ma.History, db.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    output,
			})
			ma.HistoryMu.Unlock()
			if ma.Manager != nil {
				_ = ma.Manager.SaveAgentState(ma, "running")
			}

			if consecutiveGuardRejections >= agent.ConsecutiveLimit {
				haltMsg := fmt.Sprintf("[Subagent '%s' halted: loop protection rejected %d consecutive tool calls. Stop repeating blocked actions and proceed with 'edit'/'write' or provide final response.]", ma.Name, agent.ConsecutiveLimit)
				ma.HistoryMu.Lock()
				ma.History = append(ma.History, db.Message{
					Role:    "assistant",
					Content: haltMsg,
				})
				ma.HistoryMu.Unlock()
				if ma.Manager != nil {
					_ = ma.Manager.SaveAgentState(ma, "failed")
				}
				return db.Message{
					Role:    "assistant",
					Content: haltMsg,
				}, nil
			}
		}
	}

	return db.Message{}, fmt.Errorf("reached maximum reasoning steps limit (%d)", maxSteps)
}
