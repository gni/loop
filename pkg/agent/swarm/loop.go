package swarm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

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
					if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter != nil {
						writer = ma.BaseAgent.CurrentWriter
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

						if err != context.Canceled {
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
						default:
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
					default:
					}
				}
			}
		}
	}()
}

func (ma *MultiAgent) executeLoop(ctx context.Context, w io.Writer, theme style.UITheme) (db.Message, error) {
	writer := w
	if ma.BaseAgent != nil && ma.BaseAgent.CurrentWriter != nil {
		writer = ma.BaseAgent.CurrentWriter
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

		var assistantMsg *db.Message
		go func() {
			allowlist := ma.GetToolAllowlist()
			msg, err := ma.BaseAgent.StreamChatCompletions(ctx, historyCopy, allowlist, chunkChan)
			errChan <- err
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
		}()

		if ma.BaseAgent != nil {
			ma.BaseAgent.CurrentStreamMu.Lock()
			ma.BaseAgent.CurrentStreamBuffer = new(bytes.Buffer)
			ma.BaseAgent.CurrentStreamMu.Unlock()
			teeWriter := agent.NewCustomTeeWriter(writer, ma.BaseAgent.CurrentStreamBuffer)
			writer = teeWriter
		}
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

		if ma.BaseAgent != nil {
			ma.BaseAgent.CurrentStreamMu.Lock()
			ma.BaseAgent.CurrentStreamBuffer = nil
			ma.BaseAgent.CurrentStreamMu.Unlock()
		}

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
			ma.BaseAgent.DebugLogLLMResponse("subagent:"+ma.Name, iter, assistantMsg, ma.BaseAgent.LastGenerationDuration)
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
