package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func (a *Agent) executeToolCalls(
	ctx context.Context,
	assistantMsg *db.Message,
	messages *[]db.Message,
	sr StreamRenderer,
	loader *turnLoader,
	ncw io.Writer,
	theme style.UITheme,
	guard *TurnExecutionGuard,
	sessionID string,
	iter int,
	consecutiveGuardRejections *int,
) (halted bool, aborted bool) {
	if a.Config != nil && !a.Config.ParallelToolCalls && len(assistantMsg.ToolCalls) > 1 {
		assistantMsg.ToolCalls = assistantMsg.ToolCalls[:1]
	}

	for idx, tc := range assistantMsg.ToolCalls {
		if ctx.Err() != nil {
			// Pairing invariance: an assistant turn with tool calls must have a tool
			// response for every call, otherwise EnforceToolPairingInvariance drops the
			// whole turn. Record the interruption for this call and every remaining one.
			a.recordToolCallInterruption(messages, assistantMsg.ToolCalls[idx:], "cancelled before execution", sessionID)
			return true, false
		}
		a.SetLiveBodyStreamed(false)

		isSubagent := strings.HasPrefix(tc.Function.Name, "subagent__")
		wasStreamed := sr.GetToolTitleLineNumber(idx) != -1

		if !wasStreamed {
			if idx == 0 && (strings.TrimSpace(assistantMsg.Content) != "" || strings.TrimSpace(assistantMsg.ReasoningContent) != "") {
				fmt.Fprintln(ncw)
			}
			// Render the tool header only if it wasn't already streamed
			if a.UI != nil {
				a.UI.RenderToolHeader(ncw, theme, tc.Function.Name, tc.Function.Arguments)
			} else {
				fmt.Fprintf(ncw, "tool call: %s %s\n", tc.Function.Name, tc.Function.Arguments)
			}
		}

		approved := false
		always := false
		approvalRendered := false
		// Same nil guard as the other a.Config reads in this function.
		if a.Config != nil && !a.Config.AutoApprove && NeedsApproval(tc.Function.Name) {
			approvalRendered = true
			sr.Flush()
			if a.UI != nil {
				if loader != nil {
					loader.Pause()
				}
				approved, always = a.UI.AskForApproval(unwrapWriter(ncw), theme)
				if loader != nil {
					loader.Resume()
				}
			} else {
				approved = true
			}
			if always {
				a.Config.AutoApprove = true
				if err := config.SaveConfig(a.ConfigPath, a.Config); err != nil {
					a.DebugLogError(sessionID, "config.SaveConfig (auto-approve)", err)
				}
			}
		} else {
			approved = true
		}

		if approved {
			var toolOutput string
			var toolErr error

			if guardErr := guard.CheckPreExecution(tc.Function.Name, tc.Function.Arguments); guardErr != nil {
				toolErr = guardErr
				toolOutput = guardErr.Error()
				*consecutiveGuardRejections++
				a.DebugLogRepetition(sessionID, tc.Function.Name, tc.Function.Arguments, guard.ConsecutiveIdenticalCount(), guardErr.Error())
			} else {
				*consecutiveGuardRejections = 0
				allowed, reason := a.runBeforeToolHook(tc)
				startTool := time.Now()
				if !allowed {
					toolOutput = fmt.Sprintf("Error: Tool execution blocked by before-hook: %s", reason)
					toolErr = fmt.Errorf("blocked by hook")
				} else {
					if loader != nil {
						loader.ShowDots()
					}
					toolOutput, toolErr = a.Registry.Execute(a, tc.Function.Name, tc.Function.Arguments)
					if ctx.Err() != nil {
						// Partial execution still needs the tool response for this call so
						// the assistant turn stays paired.
						a.recordToolCallInterruption(messages, assistantMsg.ToolCalls[idx:], "cancelled during execution", sessionID)
						return true, false
					}
					toolOutput, toolErr = a.runAfterToolHook(tc, toolOutput, toolErr)
				}
				toolDuration := time.Since(startTool)
				a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, toolOutput, toolErr, toolDuration)
			}

			guard.RecordPostExecution(tc.Function.Name, tc.Function.Arguments, toolOutput, toolErr)
			if reminder := guard.GetAdvisoryReminder(tc.Function.Name, tc.Function.Arguments); reminder != "" {
				toolOutput = toolOutput + "\n\n[" + reminder + "]"
			}

			if toolErr != nil {
				toolOutput = FormatToolExecutionFailure(tc.Function.Name, toolOutput, toolErr)
			}
			if toolOutput == "" {
				toolOutput = "(no output)"
			}

			// Apply deterministic output pruning and spill-to-disk to preserve context budget
			maxOutputBytes := tool.DefaultMaxToolOutputBytes
			if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
				maxOutputBytes = a.Config.MaxToolOutputBytes
			}
			prunedOutput, _, _ := tool.SpillAndPruneOutput(a.WorkspaceRoot, sessionID, tc.ID, tc.Function.Name, toolOutput, maxOutputBytes)

			if !isSubagent && !approvalRendered {
				sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
			}

			// Render the tool output
			if !isSubagent {
				bodyStreamed := sr.DidStreamToolBody(idx) || a.DidStreamLiveBody()
				if a.UI != nil {
					a.UI.RenderToolOutput(ncw, prunedOutput, toolErr != nil, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
				} else {
					if !bodyStreamed {
						fmt.Fprintln(ncw, prunedOutput)
					}
				}
			}

			// Update agent state
			a.stateMu.Lock()
			isReadOnlyTool := IsInspectionTool(tc.Function.Name)
			isPrevEdit := a.lastToolWasEdit
			if !isReadOnlyTool || !isPrevEdit || a.lastToolOutput == "" {
				a.lastToolOutput = prunedOutput
				a.lastToolIsError = toolErr != nil
				a.lastToolWasEdit = (tc.Function.Name == "edit")
			}
			a.stateMu.Unlock()

			// Append message to history
			*messages = append(*messages, db.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    prunedOutput,
			})
			if sessionID != "" {
				if err := db.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
					a.DebugLogError(sessionID, "db.SaveMessage (tool response)", err)
				}
			}

			if *consecutiveGuardRejections >= ConsecutiveLimit {
				haltNotice := fmt.Sprintf("[agent halted: loop protection rejected %d consecutive tool calls. stopping execution to prevent infinite loop. proceed with 'edit'/'write' or provide final response.]", ConsecutiveLimit)
				*messages = append(*messages, db.Message{
					Role:    "assistant",
					Content: haltNotice,
				})
				if sessionID != "" {
					if err := db.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
						a.DebugLogError(sessionID, "db.SaveMessage (halt notice)", err)
					}
				}
				if a.UI != nil {
					fmt.Fprintln(ncw, style.NewStyle().Foreground(theme.Error).Bold(true).Render(haltNotice))
				} else {
					fmt.Fprintln(ncw, haltNotice)
				}
				return true, false
			}

		} else {
			// Rejected!
			toolOutput := "error: tool execution rejected by user."
			a.stateMu.Lock()
			a.lastToolOutput = toolOutput
			a.lastToolIsError = true
			a.stateMu.Unlock()

			if !approvalRendered {
				sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
			}
			if a.UI != nil {
				a.UI.RenderToolOutput(ncw, toolOutput, true, a.Config.CollapseResults, theme, tc.Function.Name, tc.Function.Arguments, sr.DidStreamToolBody(idx))
			} else {
				fmt.Fprintln(ncw, toolOutput)
			}

			*messages = append(*messages, db.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    toolOutput,
			})
			if sessionID != "" {
				if err := db.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
					a.DebugLogError(sessionID, "db.SaveMessage (rejected tool)", err)
				}
			}

			// Abort execution of subsequent tools in the batch
			return false, true
		}
	}
	return false, false
}

// recordToolCallInterruption appends a tool response for every tool call that will not
// be executed, so the assistant turn keeps its required tool_call_id pairing. Without
// this, EnforceToolPairingInvariance drops the whole turn from the next request.
func (a *Agent) recordToolCallInterruption(messages *[]db.Message, calls []db.ToolCall, reason, sessionID string) {
	for _, tc := range calls {
		content := fmt.Sprintf("error: tool execution interrupted (%s)", reason)
		*messages = append(*messages, db.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    content,
		})
		if sessionID != "" {
			if err := db.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
				a.DebugLogError(sessionID, "db.SaveMessage (interrupted tool)", err)
			}
		}
	}
}
