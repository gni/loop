package agent

import (
	"bytes"
	"fmt"
	"io"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
	"strings"
)

func (e *TurnEngine) emitRejectedToolCall(
	idx int,
	tc db.ToolCall,
	output string,
	completeHeader bool,
	bodyStreamed bool,
	sink HistorySink,
	sr StreamRenderer,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
) {
	e.recordRejectedState(output)
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.SetResult(idx, output, fmt.Errorf("tool rejected"))
		e.policy.ToolBatch.Finish(idx, true)
	}
	if (tc.Function.Name == "write" || tc.Function.Name == "edit") && bodyStreamed {
		if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
			label := "⎿  Not saved (details below)"
			if tc.Function.Name == "edit" {
				label = "⎿  Not applied (details below)"
			}
			_ = replacer.TryCompleteWriteResult(idx, label)
		}
	}
	if recorder, ok := sr.(interface {
		RecordToolResult(int, string, string, bool)
	}); ok {
		recorder.RecordToolResult(idx, tc.Function.Arguments, output, true)
	}
	if completeHeader {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
	}
	render.ToolOutput(w, theme, output, true, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
	e.agent.emit(Event{Type: "tool_execution_end", SessionID: e.policy.SessionKey, ToolCall: &tc, Output: output, IsError: true})
	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: output, IsError: true})
}

func (e *TurnEngine) finalizeToolCall(
	idx int,
	tc db.ToolCall,
	rawOutput string,
	toolErr error,
	approvalRendered bool,
	preserveEditOutput bool,
	sink HistorySink,
	sr StreamRenderer,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
	rich tool.ToolResult,
) {
	a := e.agent
	e.agent.emit(Event{Type: "tool_execution_end", SessionID: e.policy.SessionKey, ToolCall: &tc, Output: rawOutput, IsError: toolErr != nil})
	e.guard.RecordPostExecution(tc.Function.Name, tc.Function.Arguments, rawOutput, toolErr)
	if reminder := e.guard.GetAdvisoryReminder(tc.Function.Name, tc.Function.Arguments); reminder != "" {
		rawOutput += "\n\n[" + reminder + "]"
	}
	if toolErr != nil {
		rawOutput = FormatToolExecutionFailure(tc.Function.Name, rawOutput, toolErr)
	}
	if rawOutput == "" {
		rawOutput = "(no output)"
	}

	maxOutputBytes := tool.DefaultMaxToolOutputBytes
	if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
		maxOutputBytes = a.Config.MaxToolOutputBytes
	}
	if ceiling := a.GetLimits().MaxOutputBytes; ceiling < maxOutputBytes {
		maxOutputBytes = ceiling
	}
	pruned, _, _ := tool.SpillAndPruneOutput(a.WorkspaceRoot, e.policy.SessionKey, tc.ID, tc.Function.Name, rawOutput, maxOutputBytes)
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.SetResult(idx, pruned, toolErr)
		e.policy.ToolBatch.Finish(idx, toolErr != nil)
	}

	if recorder, ok := sr.(interface {
		RecordToolResult(int, string, string, bool)
	}); ok {
		recorder.RecordToolResult(idx, tc.Function.Arguments, pruned, toolErr != nil)
	}

	isSubagentCall := strings.HasPrefix(tc.Function.Name, "subagent__")
	if !isSubagentCall && !approvalRendered {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
	}
	if !isSubagentCall {
		livePreviewed := sr.DidStreamToolBody(idx)
		bodyStreamed := livePreviewed || (preserveEditOutput && a.DidStreamLiveBody())
		// The proposed edit was streamed as +/- lines. Render the verified
		// one-line result beside that preview, not a duplicate full diff.
		// On terminal resize, history intentionally shows the authoritative
		// diff instead of the provisional argument preview.
		if tc.Function.Name == "edit" {
			bodyStreamed = false
		}
		resultHandled := false
		if livePreviewed && (tc.Function.Name == "write" || tc.Function.Name == "edit") {
			if toolErr != nil {
				if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
					_ = replacer.TryCompleteWriteResult(idx, "⎿  Error (details below)")
				}
			} else {
				var rendered bytes.Buffer
				render.ToolOutput(&rendered, theme, pruned, false, tc.Function.Name, tc.Function.Arguments, tc.Function.Name == "write")
				if line := verifiedToolSummaryLine(rendered.String()); line != "" {
					if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
						resultHandled = replacer.TryCompleteWriteResult(idx, line)
					}
					if !resultHandled && e.policy.InlineEarlyResults {
						// Executing beside this exact preview: a terminal without
						// tracked rows can safely append the verified summary here.
						fmt.Fprintln(w, line)
						resultHandled = true
					} else if !e.policy.InlineEarlyResults {
						// Old previews may have scrolled away; never print their
						// result under an unrelated later file.
						resultHandled = true
					}
				}
			}
		}
		if !resultHandled {
			render.ToolOutput(w, theme, pruned, toolErr != nil, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
		}
	}

	a.stateMu.Lock()
	if !preserveEditOutput || !IsInspectionTool(tc.Function.Name) || !a.lastToolWasEdit || a.lastToolOutput == "" {
		a.lastToolOutput = pruned
		a.lastToolIsError = toolErr != nil
		a.lastToolWasEdit = tc.Function.Name == "edit"
	}
	a.stateMu.Unlock()

	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: pruned, ContentBlocks: rich.Content, Structured: rich.Structured, IsError: toolErr != nil})
	if afterCall != nil {
		afterCall(tc, pruned, toolErr)
	}
}

// verifiedToolSummaryLine extracts the final ⎿ summary from normal tool
// rendering. It must never return an unverified preview or a multiline diff.
func verifiedToolSummaryLine(rendered string) string {
	lines := strings.Split(strings.TrimRight(rendered, "\r\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	line := lines[len(lines)-1]
	if strings.HasPrefix(strings.TrimSpace(style.StripAnsi(line)), "⎿") {
		return line
	}
	return ""
}

func (e *TurnEngine) emitHaltNotice(sink HistorySink, w io.Writer, theme style.UITheme) {
	haltNotice := e.HaltNotice()
	sink.Append(db.Message{Role: "assistant", Content: haltNotice})
	if e.agent != nil && e.agent.UI != nil {
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render(haltNotice))
	} else {
		fmt.Fprintln(w, haltNotice)
	}
}
