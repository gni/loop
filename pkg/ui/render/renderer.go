package render

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"loop/pkg/agent"
	"loop/pkg/ui/style"
)

// StreamOptions contains presentation settings and an optional UI preparation hook.
// The renderer owns no interactive UI state.
type StreamOptions struct {
	Markdown         bool
	ShowMicroseconds bool
	BeforeWrite      func()
}

type StreamRenderer struct {
	beforeWrite               func()
	mu                        sync.Mutex
	w                         io.Writer
	theme                     style.UITheme
	inThinking                bool
	showThinking              bool
	showMicroseconds          bool
	reasoningStart            time.Time
	reasoningHasText          bool
	reasoningEndedWithNewline bool
	reasoningDuration         float64
	reasoningResetSequence    string
	reasoningSummaryLine      int
	reasoningCollapsed        bool
	reasoningNewlines         int
	reasoningLineCount        int
	reasoningSummaryLines     int
	pendingThoughtTextGap     bool
	prompt                    string
	echoFilter                *agent.PromptEchoFilter

	lastEndedWithNewline bool
	markdownEnabled      bool
	hasWrittenText       bool
	hasWrittenThoughts   bool
	parser               *JSONStreamParser
	live                 *LiveMarkdownRenderer
	writeResultLines     map[int]int
	sequentialResults    bool
	toolStates           map[string]agent.ToolLifecycleEvent
	toolIDs              map[int]string
	chatDraftLine        int
	chatDraftVisible     bool
	firstToolName        string
	livePreviewLine      int
	livePreviewVisible   bool
	livePreviewSnapshot  string
	liveCallViews        map[int]*liveCallView
	liveActiveCall       int
	liveVisibleCall      int
	liveResultGap        bool
	liveCollapse         bool
	interactiveQuestions bool
	// Recorded model-visible fragments for active-turn redraws. A full
	// history replay uses these instead of the terminal write byte stream.
	liveReplayEvents       []liveReplayEvent
	liveReplayTextFlushed  bool
	trackedRowsInvalidated bool
}

func NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, options StreamOptions) *StreamRenderer {
	sr := &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		markdownEnabled:      options.Markdown,
		showMicroseconds:     options.ShowMicroseconds,
		beforeWrite:          options.BeforeWrite,
		lastEndedWithNewline: true,
		live:                 NewLiveMarkdownRenderer(w, theme),
		writeResultLines:     make(map[int]int),
		toolStates:           make(map[string]agent.ToolLifecycleEvent),
		toolIDs:              make(map[int]string),
		chatDraftLine:        -1,
		livePreviewLine:      -1,
		liveCallViews:        make(map[int]*liveCallView),
		liveActiveCall:       -1,
		reasoningSummaryLine: -1,
	}
	sr.parser = NewJSONStreamParser(streamWrites)
	return sr
}

// ObserveToolLifecycle keeps the renderer's per-call state keyed by the
// stable local ID. Provider indices are only used to look up the owning call.
func (sr *StreamRenderer) ObserveToolLifecycle(event agent.ToolLifecycleEvent) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.toolStates[event.CallID] = event
	sr.toolIDs[event.Index] = event.CallID
}

// ShowToolDraft renders deferred tool generation in the conversation output,
// never in the pinned token or elapsed-time status line. It keeps one mutable
// preview row, so repeated argument chunks do not flood scrollback. All writes
// still execute only after validated tool calls are returned by the model.
func (sr *StreamRenderer) ShowToolDraft(index int, name, excerpt string, size int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.trackedRowsInvalidated || !sr.sequentialResults || !CanReplaceTrackedStreamLine(sr.w) {
		return
	}
	// Do not allow model-provided tool names to introduce terminal controls.
	name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return r
		}
		return -1
	}, name)
	if name == "" {
		name = "tool"
	}
	escape := false
	clean := strings.Map(func(r rune) rune {
		if r == '\x1b' {
			escape = true
			return -1
		}
		if escape {
			if unicode.IsLetter(r) || r == '\a' {
				escape = false
			}
			return -1
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, excerpt)
	clean = strings.Join(strings.Fields(clean), " ")
	maxExcerpt := 72
	if width, _ := style.GetTerminalSize(); width > 0 && width < 100 {
		maxExcerpt = width - 28
	}
	if maxExcerpt < 0 {
		maxExcerpt = 0
	}
	runes := []rune(clean)
	if len(runes) > maxExcerpt {
		clean = "…" + string(runes[len(runes)-maxExcerpt:])
	}
	label := fmt.Sprintf("↳ receiving %s #%d (%d bytes)", name, index+1, size)
	if clean != "" && maxExcerpt > 0 && name == "write" {
		label = fmt.Sprintf("↳ receiving write #%d: %s", index+1, clean)
	}
	line := style.NewStyle().Foreground(sr.theme.Border).Render(label)
	if sr.chatDraftVisible {
		if ReplaceTrackedStreamLine(sr.w, sr.chatDraftLine, line) {
			return
		}
		// The row has scrolled out of the visible region. A stale row cannot
		// be changed safely, so do not print a duplicate progress row.
		return
	}
	// The first tool's JSON must be complete before this method is called.
	// Flush its unfinished body so progress never interrupts file content.
	sr.flushActiveToolLocked()
	position := GetNewlineCount(sr.w)
	if position < 0 {
		return
	}
	fmt.Fprintln(sr.w, line)
	sr.chatDraftLine = position
	sr.chatDraftVisible = true
}

// ClearToolDraft ends live generation. For a first write, its preview row
// becomes that call's inline completion slot. Other calls clear the temporary
// text before execution; their actual output is rendered by the engine.
func (sr *StreamRenderer) ClearToolDraft() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if !sr.chatDraftVisible {
		return
	}
	firstBodyStreamed := sr.parser != nil && len(sr.parser.ToolBodyStreamed) > 0 && sr.parser.ToolBodyStreamed[0]
	if sr.firstToolName == "write" && sr.sequentialResults && firstBodyStreamed {
		if ReplaceTrackedStreamLine(sr.w, sr.chatDraftLine, "⎿  writing...") {
			sr.writeResultLines[0] = sr.chatDraftLine
		}
	} else {
		ReplaceTrackedStreamLine(sr.w, sr.chatDraftLine, "")
	}
	sr.chatDraftVisible = false
	sr.chatDraftLine = -1
}

// SetSequentialToolResults disables speculative result rows. Tool output then
// follows the owning preview directly, even if a previous preview scrolled out
// of the terminal's redrawable region.
func (sr *StreamRenderer) SetSequentialToolResults(enabled bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.sequentialResults = enabled
}

// InvalidateTrackedRows is called after an external full-screen replay.
// Old terminal row indexes are no longer valid, even though stream parsing
// state and the already displayed content must continue unchanged.
func (sr *StreamRenderer) InvalidateTrackedRows() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.trackedRowsInvalidated = true
	sr.chatDraftVisible = false
	sr.chatDraftLine = -1
	sr.livePreviewVisible = false
	sr.livePreviewLine = -1
	sr.reasoningSummaryLine = -1
	if sr.inThinking {
		sr.reasoningCollapsed = sr.liveCollapse
		if sr.liveCollapse {
			sr.reasoningSummaryLines = sr.reasoningLineCount
			if position := GetNewlineCount(sr.w); position > 0 {
				sr.reasoningSummaryLine = position - 1
			}
			sr.reasoningEndedWithNewline = true
		} else if len(sr.liveReplayEvents) > 0 {
			last := sr.liveReplayEvents[len(sr.liveReplayEvents)-1]
			sr.reasoningEndedWithNewline = strings.HasSuffix(last.text, "\n")
		}
	}
	// Only the last unfinished compact call can still be updated. Its
	// heading and progress row are the final two rows of the replay.
	compactIndex := -1
	if sr.liveCollapse && len(sr.liveReplayEvents) > 0 {
		last := sr.liveReplayEvents[len(sr.liveReplayEvents)-1]
		if last.kind == "tool" {
			if view := sr.liveCallViews[last.index]; view != nil && !view.completed {
				compactIndex = last.index
			}
		}
	}
	_, compactResultPending := sr.writeResultLines[compactIndex]
	clear(sr.writeResultLines)
	for _, view := range sr.liveCallViews {
		view.titleLine = -1
		// A snapshot already shows the compact row for a collapsed call.
		// Never print a second preview row on the next argument fragment.
		view.compactLine = -1
		if sr.liveCollapse && view.headerShown {
			view.compactLine = -2
		}
	}
	if view := sr.liveCallViews[compactIndex]; view != nil {
		if position := GetNewlineCount(sr.w); position >= 2 {
			view.titleLine = position - 2
			view.compactLine = position - 1
			if compactResultPending {
				sr.writeResultLines[compactIndex] = view.compactLine
			}
		}
	}
	// Resume the exact last source line, including content that was hidden
	// before expanding. The parser and tool execution state stay intact.
	if view := sr.liveCallViews[sr.liveActiveCall]; view != nil {
		view.codeLine.Reset()
		view.codeLineOverflow = false
		view.endsWithNewline = true
		if !sr.liveCollapse {
			content := view.fullContent.String()
			view.bytes = view.receivedBytes
			view.endsWithNewline = content == "" || strings.HasSuffix(content, "\n")
			if !view.endsWithNewline {
				tail := content[strings.LastIndexByte(content, '\n')+1:]
				if len(tail) <= 4096 {
					view.codeLine.WriteString(tail)
				} else {
					view.codeLineOverflow = true
				}
			}
		}
	}
	if sr.parser != nil {
		for i := range sr.parser.ToolTitleLineNumbers {
			sr.parser.ToolTitleLineNumbers[i] = -1
		}
	}
}

// SafeToReplay reports whether semantic replay covers the active output.
// The legacy JSON parser can hold tool text outside the replay transcript.
func (sr *StreamRenderer) SafeToReplay() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.parser == nil || sr.parser.ActiveToolName == "" || sr.parser.ActiveToolName == "task_state"
}

func (sr *StreamRenderer) HasOutput() bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.hasWrittenText || sr.hasWrittenThoughts
}

func (sr *StreamRenderer) getThinkingStyle() style.Style {
	border := sr.theme.Border
	if border == nil {
		border = style.Color("#4C566A")
	}
	return style.NewStyle().Foreground(border).Italic(true)
}

func (sr *StreamRenderer) SetPrompt(prompt string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.prompt = strings.TrimSpace(prompt)
	if sr.prompt != "" {
		sr.echoFilter = agent.NewPromptEchoFilter(sr.prompt)
	} else {
		sr.echoFilter = nil
	}
}

func (sr *StreamRenderer) WriteReasoning(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if !sr.showThinking || chunk == "" {
		return
	}

	if sr.echoFilter != nil {
		chunk = sr.echoFilter.Write(chunk)
		if chunk == "" {
			return
		}
	}

	if !sr.reasoningHasText && strings.TrimSpace(chunk) == "" {
		return
	}

	sr.writeReasoningChunk(chunk)
}

func (sr *StreamRenderer) writeReasoningChunk(chunk string) {
	sr.checkFirstWrite()
	if sr.liveActiveCall >= 0 {
		if view := sr.liveCallViews[sr.liveActiveCall]; view != nil {
			sr.endLiveCallLocked(sr.liveActiveCall, view)
			fmt.Fprint(sr.w, "\n")
		}
		sr.liveActiveCall = -1
	}

	dimStyle := sr.getThinkingStyle()
	startSeq, resetSeq := dimStyle.GetSequence()

	if !sr.inThinking {
		sr.inThinking = true
		sr.reasoningStart = time.Now()
		sr.reasoningHasText = false
		sr.reasoningEndedWithNewline = false
		sr.pendingThoughtTextGap = false
		sr.reasoningResetSequence = resetSeq
		sr.reasoningSummaryLine = -1
		sr.reasoningCollapsed = false
		sr.reasoningNewlines = 0
		sr.reasoningLineCount = 0
		sr.reasoningSummaryLines = 0
	}

	hadReasoningText := sr.reasoningHasText
	sr.hasWrittenThoughts = true
	sr.reasoningHasText = true
	sr.appendLiveReplayTextLocked("reasoning", chunk)
	sr.reasoningNewlines += strings.Count(chunk, "\n")
	sr.reasoningLineCount = sr.reasoningNewlines
	if !strings.HasSuffix(chunk, "\n") {
		sr.reasoningLineCount++
	}
	if sr.liveCollapse {
		if !sr.reasoningCollapsed {
			if hadReasoningText && !sr.reasoningEndedWithNewline {
				fmt.Fprint(sr.w, "\n")
			}
			sr.reasoningSummaryLine = GetNewlineCount(sr.w)
			fmt.Fprintln(sr.w, FormatThinkingSummary(sr.theme, 0, true, sr.reasoningLineCount, sr.showMicroseconds))
			sr.reasoningCollapsed = true
		} else if sr.reasoningLineCount != sr.reasoningSummaryLines {
			if !ReplaceTrackedStreamLine(sr.w, sr.reasoningSummaryLine, FormatThinkingSummary(sr.theme, 0, true, sr.reasoningLineCount, sr.showMicroseconds)) {
				sr.requestLiveSyntaxRefreshLocked()
			}
		}
		sr.reasoningSummaryLines = sr.reasoningLineCount
		sr.reasoningEndedWithNewline = true
		return
	}
	sr.reasoningEndedWithNewline = strings.HasSuffix(chunk, "\n")
	fmt.Fprint(sr.w, startSeq+chunk+resetSeq)
}

func (sr *StreamRenderer) EndThinking() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()
}

func (sr *StreamRenderer) endThinking() {
	if sr.echoFilter != nil {
		rem := sr.echoFilter.Flush()
		sr.echoFilter = nil
		if rem != "" && strings.TrimSpace(rem) != "" {
			sr.writeReasoningChunk(rem)
		}
	}

	if !sr.inThinking {
		return
	}
	sr.inThinking = false

	if sr.reasoningResetSequence != "" {
		fmt.Fprint(sr.w, sr.reasoningResetSequence)
		sr.reasoningResetSequence = ""
	}
	if !sr.reasoningHasText {
		return
	}
	if !sr.reasoningEndedWithNewline {
		fmt.Fprint(sr.w, "\n")
	}

	elapsed := time.Since(sr.reasoningStart).Seconds()
	sr.reasoningDuration = elapsed
	for i := len(sr.liveReplayEvents) - 1; i >= 0; i-- {
		if sr.liveReplayEvents[i].kind == "reasoning" {
			sr.liveReplayEvents[i].duration = elapsed
			break
		}
	}
	labelStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)

	if sr.reasoningCollapsed {
		if !ReplaceTrackedStreamLine(sr.w, sr.reasoningSummaryLine, FormatThinkingSummary(sr.theme, elapsed, false, sr.reasoningLineCount, sr.showMicroseconds)) {
			sr.requestLiveSyntaxRefreshLocked()
		}
	} else {
		fmt.Fprintf(sr.w, "%s\n", labelStyle.Render(fmt.Sprintf("thought (%s)", style.FormatElapsedDuration(time.Duration(elapsed*float64(time.Second)), sr.showMicroseconds))))
	}
	sr.reasoningSummaryLine = -1
	sr.reasoningHasText = false
	sr.reasoningEndedWithNewline = true
	sr.pendingThoughtTextGap = true
	sr.lastEndedWithNewline = true
	sr.live.ResetAtLineStart()
}

func (sr *StreamRenderer) Write(chunk string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.endThinking()

	// The UI owns spacing before an answer. Some model templates emit
	// leading newlines, including in separate chunks after </think>.
	// Keep indentation and all newlines within the answer unchanged.
	if !sr.hasWrittenText || sr.pendingThoughtTextGap {
		chunk = strings.TrimLeft(chunk, "\r\n")
	}
	if chunk == "" {
		return
	}
	if sr.liveActiveCall >= 0 {
		if view := sr.liveCallViews[sr.liveActiveCall]; view != nil {
			sr.endLiveCallLocked(sr.liveActiveCall, view)
			fmt.Fprint(sr.w, "\n")
		}
		sr.liveActiveCall = -1
	}

	if sr.pendingThoughtTextGap {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	sr.checkFirstWrite()
	sr.hasWrittenText = true
	sr.appendLiveReplayTextLocked("text", chunk)
	sr.liveReplayTextFlushed = false
	if !sr.markdownEnabled {
		fmt.Fprint(sr.w, chunk)
		sr.lastEndedWithNewline = strings.HasSuffix(chunk, "\n")
		return
	}
	sr.live.Write(chunk)
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
}

func (sr *StreamRenderer) finishLiveTextLocked() {
	sr.liveReplayTextFlushed = true
	if !sr.markdownEnabled {
		if sr.hasWrittenText && !sr.lastEndedWithNewline {
			fmt.Fprint(sr.w, "\n")
			sr.lastEndedWithNewline = true
		}
		return
	}
	if sr.hasWrittenText {
		sr.live.EnsureTrailingNewline()
	} else {
		sr.live.Flush()
	}
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
}

func (sr *StreamRenderer) Flush() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.flushLocked()
}

func (sr *StreamRenderer) flushLocked() {
	sr.liveReplayTextFlushed = true
	sr.endThinking()
	if sr.pendingThoughtTextGap && sr.parser != nil && sr.parser.ActiveToolName != "" {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	sr.pendingThoughtTextGap = false
	// The terminal already reserves one blank row above the stats line.
	// Finalizing streamed text must not emit another blank terminal row.
	// StartToolCall still uses finishLiveTextLocked to separate text and tools.
	if !sr.markdownEnabled {
		sr.flushActiveToolLocked()
		return
	}
	sr.live.Flush()
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
	sr.flushActiveToolLocked()
}

func (sr *StreamRenderer) flushActiveToolLocked() {
	if sr.parser != nil && sr.parser.ActiveToolName == "task_state" {
		sr.parser.ActiveToolName = ""
		sr.parser.ActiveToolIndex = -1
		return
	}
	if sr.parser != nil && sr.parser.ActiveToolName != "" {
		if !sr.parser.TitlePrinted && sr.parser.CanPrintTitle() {
			sr.parser.PrintStreamTitle(sr.w, sr.theme)
		}
		sr.parser.FlushOutputBuf(sr.w, sr.theme)
		sr.parser.FlushContentLine(sr.w, sr.theme)
		if sr.parser.ActiveToolName == "write" && sr.parser.ActiveToolIndex >= 0 &&
			sr.parser.ActiveToolIndex < len(sr.parser.ToolBodyStreamed) &&
			sr.parser.ToolBodyStreamed[sr.parser.ActiveToolIndex] {
			if !sr.parser.BodyEndsWithNewline {
				fmt.Fprint(sr.w, "\n")
				sr.parser.BodyEndsWithNewline = true
			}
			// The model can finish several calls before any tool executes.
			// Reserve a result row beside this preview and replace it after
			// the matching tool has actually completed. Never write a file here.
			if !sr.sequentialResults && CanReplaceTrackedStreamLine(sr.w) {
				if line := GetNewlineCount(sr.w); line >= 0 {
					sr.writeResultLines[sr.parser.ActiveToolIndex] = line
					fmt.Fprintln(sr.w, style.NewStyle().Foreground(sr.theme.Border).Render("⎿  writing..."))
				}
			}
		}
		sr.parser.IsRawStream = false
		sr.parser.ActiveToolName = ""
		sr.parser.ActiveToolIndex = -1
	}
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
	if toolCallIndex == 0 {
		// Unnamed provider arguments may produce an early chat progress row.
		// Remove it before rendering the first real tool title.
		if sr.chatDraftVisible && sr.firstToolName == "" {
			ReplaceTrackedStreamLine(sr.w, sr.chatDraftLine, "")
			sr.chatDraftVisible = false
			sr.chatDraftLine = -1
		}
		sr.firstToolName = toolName
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
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.parser != nil && sr.parser.ActiveToolName == "task_state" {
		return
	}

	if len(content) > 0 {
		sr.checkFirstWrite()
	}

	if sr.parser != nil {
		if sr.parser.IsRawStream {
			sr.parser.FeedRaw(content, sr.w, sr.theme)
		} else {
			sr.parser.Feed(content, sr.w, sr.theme)
		}
	}
}

func (sr *StreamRenderer) checkFirstWrite() {
	if sr.beforeWrite != nil {
		sr.beforeWrite()
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
			ReplaceTrackedStreamLine(sr.w, view.titleLine, FormatToolTitle(RenderToolSymbol(toolName, status, sr.theme), toolName, safeLiveLabel(target), sr.theme))
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
	ReplaceTrackedStreamLine(sr.w, sr.parser.ToolTitleLineNumbers[index], FormatToolTitle(symbol, toolName, target, sr.theme))
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

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}
