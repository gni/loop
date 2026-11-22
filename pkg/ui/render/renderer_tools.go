package render

import (
	"fmt"
	"strings"

	"loop/pkg/agent"
)

// ObserveToolLifecycle keeps the renderer's per-call state keyed by the
// stable local ID. Provider indices are only used to look up the owning call.
func (sr *StreamRenderer) ObserveToolLifecycle(event agent.ToolLifecycleEvent) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.toolStates[event.CallID] = event
	sr.toolIDs[event.Index] = event.CallID
}

// SetSequentialToolResults disables speculative result rows. Tool output then
// follows the owning preview directly, even if a previous preview scrolled out
// of the terminal's redrawable region.
func (sr *StreamRenderer) SetSequentialToolResults(enabled bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.sequentialResults = enabled
}

func (sr *StreamRenderer) StartToolCall(toolName string, toolCallIndex int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if toolName == "task_state" {
		sr.trackProgressCallLocked(toolCallIndex)
		sr.endThinking()
		sr.finishLiveTextLocked()
		sr.flushActiveToolLocked()
		if sr.parser != nil {
			sr.parser.ActiveToolName = toolName
			sr.parser.ActiveToolIndex = toolCallIndex
		}
		return
	}

	sr.endThinking()
	if sr.pendingThoughtTextGap {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	// finishLiveTextLocked already ends the prior text on a fresh line.
	// Do not add a second newline before the streamed tool title.
	hadText := sr.hasWrittenText
	sr.finishLiveTextLocked()
	if hadText {
		// Separate assistant prose from any following tool, not just bash.
		fmt.Fprint(sr.w, "\n")
	}
	sr.hasWrittenText = false

	if sr.parser != nil {
		if sr.parser.ActiveToolName != "" && sr.parser.ActiveToolIndex == toolCallIndex {
			sr.parser.ActiveToolName = toolName
			return
		}
		if sr.parser.ActiveToolName != "" {
			previousWrite := sr.parser.ActiveToolName == "write" && sr.parser.TitlePrinted
			sr.flushActiveToolLocked()
			if previousWrite && toolName == "write" {
				// The previous preview has ended its line; leave one empty row
				// before starting another file's live content.
				fmt.Fprint(sr.w, "\n")
			}
		}
		sr.parser.ActiveToolIndex = toolCallIndex
		sr.parser.EnsureTrackingIndex()
		sr.parser.ToolTitleLineNumbers[toolCallIndex] = -1
		sr.parser.ToolTitlePrinted[toolCallIndex] = false
		sr.parser.ToolBodyStreamed[toolCallIndex] = false
		sr.parser.ActiveToolName = toolName
		sr.parser.TitlePrinted = false
		sr.parser.Path = ""
		sr.parser.PathPrinted = false
		sr.parser.IsContent = false
		sr.parser.IsPath = false
		sr.parser.IsRawStream = false
		sr.parser.OutputBuf.Reset()
		sr.parser.LineBuffer.Reset()
		sr.parser.InString = false
		sr.parser.InEscape = false
		sr.parser.resetEscapeState()
		sr.parser.CurrentKey = ""
		sr.parser.InValue = false
		sr.parser.Buf.Reset()
		sr.parser.GuessedLang = ""
		sr.parser.BodyEndsWithNewline = false
		sr.parser.NeedsLeadingNewline = false

		// Show a provisional write title as soon as the tool name arrives.
		// Waiting for its path or first content chunk can leave users staring
		// at an idle terminal while the model is actively generating.
		if sr.parser.StreamWrites && sr.parser.CanPrintTitle() {
			sr.parser.PrintStreamTitle(sr.w, sr.theme)
		}
	}
}

func (sr *StreamRenderer) SetToolTarget(target string, toolCallIndex int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.parser != nil {
		if sr.parser.ActiveToolName == "task_state" {
			return
		}
		sr.parser.Path = target
		sr.parser.IsRawStream = true
		if sr.parser.GuessedLang == "" && target != "" {
			sr.parser.GuessedLang = DetectLangFromPath(target)
		}
		if sr.parser.TitlePrinted {
			sr.parser.PathPrinted = true
			sr.parser.UpdateStreamTitleWithPath(sr.w, sr.theme)
		} else if sr.parser.StreamWrites {
			sr.parser.PrintStreamTitle(sr.w, sr.theme)
			sr.parser.PathPrinted = true
		}
	}
}

func (sr *StreamRenderer) WriteToolCall(content string) {
	if len(content) > 0 {
		sr.runBeforeWrite()
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.parser != nil && sr.parser.ActiveToolName == "task_state" {
		return
	}

	if sr.parser != nil {
		if sr.parser.IsRawStream {
			sr.parser.FeedRaw(content, sr.w, sr.theme)
		} else {
			sr.parser.Feed(content, sr.w, sr.theme)
		}
	}
}

func (sr *StreamRenderer) GetToolTitleLineNumber(index int) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if view := sr.liveCallViews[index]; view != nil && view.headerShown {
		return 0
	}
	if sr.parser == nil || index < 0 || index >= len(sr.parser.ToolTitleLineNumbers) {
		return -1
	}
	// Line counts can be unavailable for direct terminal writers. Remember
	// that a title was printed even after Flush() switches the active index.
	if sr.parser.ToolTitlePrinted[index] && sr.parser.ToolTitleLineNumbers[index] < 0 {
		return 0
	}
	if sr.parser.TitlePrinted && sr.parser.ActiveToolIndex == index && sr.parser.ToolTitleLineNumbers[index] < 0 {
		return 0
	}
	return sr.parser.ToolTitleLineNumbers[index]
}

func (sr *StreamRenderer) DidStreamToolBody(index int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if view := sr.liveCallViews[index]; view != nil && (view.name == "write" || view.name == "edit" || view.name == "todo") && (view.bytes > 0 || view.compactLine != -1) {
		return true
	}
	return sr.parser != nil && index >= 0 && index < len(sr.parser.ToolBodyStreamed) && sr.parser.ToolBodyStreamed[index]
}

func (sr *StreamRenderer) CompleteToolCall(index int, toolName string, toolArgs string, isError bool) {
	if toolName == "task_state" {
		return
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if id, tracked := sr.toolIDs[index]; tracked {
		// Do not update the title of a different call if the provider's
		// streamed indices were inconsistent with its final response.
		if current, exists := sr.toolStates[id]; exists && current.Name != "" && current.Name != toolName {
			return
		}
	}
	width := renderWidth(sr.w)
	if view := sr.liveCallViews[index]; view != nil && view.headerShown {
		view.completed = true
		view.failed = isError
		target := ExtractToolTarget(toolName, toolArgs)
		if target == "" {
			target = view.path
		}
		if toolName == "ask_user" {
			// The menu owns the visible question. Keep it for semantic
			// history without rewriting its generic heading after the menu.
			view.path = target
			return
		}
		status := ToolStatusSuccess
		if isError {
			status = ToolStatusError
		}
		if target != "" && view.titleLine >= 0 && toolName != "bash" {
			ReplaceTrackedStreamLine(sr.w, view.titleLine, formatToolTitleForWidth(RenderToolSymbol(toolName, status, sr.theme), toolName, safeLiveLabel(target), sr.theme, width))
		}
		return
	}
	if sr.parser == nil || index < 0 || index >= len(sr.parser.ToolTitleLineNumbers) {
		return
	}
	if sr.parser.ToolTitleLineNumbers[index] < 0 {
		return
	}
	target := ExtractToolTarget(toolName, toolArgs)
	if target == "" && sr.parser.Path != "" {
		target = sr.parser.Path
	}
	if strings.TrimSpace(target) == "" {
		return
	}
	status := ToolStatusSuccess
	if isError {
		status = ToolStatusError
	}
	symbol := RenderToolSymbol(toolName, status, sr.theme)
	if toolName == "bash" || IsCommandLikeTool(toolName) {
		ReplaceTrackedStreamLine(sr.w, sr.parser.ToolTitleLineNumbers[index], FormatBashCommandLine(symbol, target, sr.theme))
		return
	}
	ReplaceTrackedStreamLine(sr.w, sr.parser.ToolTitleLineNumbers[index], formatToolTitleForWidth(symbol, toolName, target, sr.theme, width))
}

// HasPendingWriteResult reports whether a streamed write or edit has an inline completion row.
func (sr *StreamRenderer) HasPendingWriteResult(index int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	_, ok := sr.writeResultLines[index]
	return ok
}

// TryCompleteWriteResult resolves the reserved row belonging to this tool
// index. If it scrolled offscreen, the renderer still forgets the slot.
func (sr *StreamRenderer) TryCompleteWriteResult(index int, line string) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	position, exists := sr.writeResultLines[index]
	if view := sr.liveCallViews[index]; view != nil {
		view.result = line
	}
	if !exists || strings.ContainsAny(line, "\r\n") {
		return false
	}
	// This call has finished even if its row scrolled offscreen. Forget the
	// slot so deferred cleanup cannot mislabel a successful operation as
	// "Not executed" during a later resize or terminal repaint.
	delete(sr.writeResultLines, index)
	return ReplaceTrackedStreamLine(sr.w, position, line)
}

// MarkPendingWriteResults prevents unfinished preview rows from reporting a
// successful save when execution was interrupted or denied.
func (sr *StreamRenderer) MarkPendingWriteResults(label string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	for index, position := range sr.writeResultLines {
		if ReplaceTrackedStreamLine(sr.w, position, "⎿  "+label) {
			delete(sr.writeResultLines, index)
		}
	}
}
