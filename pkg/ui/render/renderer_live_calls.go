package render

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"loop/pkg/ui/style"
)

// liveCallView records what the terminal has already displayed for a call.
// Tool arguments can be previewed during generation. Execution
// results for all tools are published by the engine, never by this preview.
type liveCallView struct {
	name             string
	path             string
	titleLine        int
	headerShown      bool
	lastRenderedPath string
	bytes            int // Bytes actually shown, not hidden by collapse.
	receivedBytes    int
	compactTail      []rune
	compactLine      int // -1: no row; -2: emitted but not trackable.
	endsWithNewline  bool
	codeLine         strings.Builder
	codeLineOverflow bool
	language         string
	fullContent      strings.Builder // Semantic source, including hidden chunks.
	result           string
	output           string
	arguments        string
	resultRecorded   bool
	completed        bool
	failed           bool
}

// SetLiveCollapse changes the presentation of subsequent tool chunks without
// restarting the stream or changing the arguments sent to the executor.
func (sr *StreamRenderer) SetLiveCollapse(collapsed bool) {
	sr.mu.Lock()
	sr.liveCollapse = collapsed
	sr.mu.Unlock()
}

// SetInteractiveQuestions gives the question menu ownership of question text.
// Automatic answers still need their question displayed by the live preview.
func (sr *StreamRenderer) SetInteractiveQuestions(enabled bool) {
	sr.mu.Lock()
	sr.interactiveQuestions = enabled
	sr.mu.Unlock()
}

// safeLiveLabel strips model-supplied cursor controls and line breaks. A tool
// argument must never be able to manipulate the terminal outside the body.
func safeLiveLabel(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if r == '\x1b' || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 240 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// ShowLiveToolContent is called in model-stream order. It never writes a file
// and never runs a tool. It appends actual decoded text deltas, rather than
// rewriting an arbitrarily sized block of terminal rows. File content received
// before its path waits until a heading can identify it above the body.
func (sr *StreamRenderer) ShowLiveToolContent(index int, name, path, delta string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	name = safeLiveLabel(name)
	if name == "" {
		return
	}
	if name == "ask_user" && sr.interactiveQuestions {
		path, delta = "", ""
	}
	if sr.liveCallViews == nil {
		sr.liveCallViews = make(map[int]*liveCallView)
	}
	view := sr.liveCallViews[index]
	if view == nil {
		view = &liveCallView{name: name, titleLine: -1, compactLine: -1, endsWithNewline: true}
		sr.liveCallViews[index] = view
	}
	path = safeLiveLabel(path)
	if path != "" {
		view.path = path
	}
	// A call whose preview was closed by early execution or a later call
	// must not reopen when the provider sends trailing argument fragments.
	if view.headerShown && sr.liveActiveCall != index {
		return
	}
	if name == "bash" && delta == "" && path != "" && view.fullContent.Len() == 0 {
		delta = path
	}
	view.receivedBytes += len(delta)
	view.fullContent.WriteString(delta)
	if !view.headerShown && (name == "write" || name == "edit") {
		if view.path == "" {
			return
		}
		// Reuse the semantic content buffer rather than keeping another
		// copy. A late filename must not require a second heading below
		// the body or a rewrite of terminal rows that scrolled away.
		delta = view.fullContent.String()
	}
	if !view.headerShown {
		sr.endThinking()
		if sr.pendingThoughtTextGap {
			fmt.Fprint(sr.w, "\n")
			sr.pendingThoughtTextGap = false
		} else if sr.hasWrittenText {
			// Assistant prose and the following tool are separate blocks.
			sr.finishLiveTextLocked()
			fmt.Fprint(sr.w, "\n")
		}
		sr.finishLiveTextLocked()
		sr.hasWrittenText = false
		if sr.liveActiveCall >= 0 {
			if old := sr.liveCallViews[sr.liveActiveCall]; old != nil {
				sr.endLiveCallLocked(sr.liveActiveCall, old)
				// The reserved row becomes the preceding call's result.
				// Leave a blank separator before the next preview.
				fmt.Fprint(sr.w, "\n")
			}
		} else if sr.liveResultGap {
			// Early execution already closed the previous preview to publish
			// its verified result. Add the same separator as the normal path.
			fmt.Fprint(sr.w, "\n")
		}
		sr.liveResultGap = false
		title := sr.liveToolTitle(name, view.path)
		view.titleLine = GetNewlineCount(sr.w)
		if name == "bash" {
			fmt.Fprint(sr.w, title)
		} else {
			fmt.Fprintln(sr.w, title)
		}
		view.headerShown = true
		sr.liveReplayEvents = append(sr.liveReplayEvents, liveReplayEvent{kind: "tool", index: index})
		view.lastRenderedPath = view.path
		view.endsWithNewline = name != "bash"
		sr.liveActiveCall = index
		sr.liveVisibleCall = index
	} else if view.path != "" && view.path != view.lastRenderedPath {
		if ReplaceTrackedStreamLine(sr.w, view.titleLine, sr.liveToolTitle(name, view.path)) {
			view.lastRenderedPath = view.path
		}
	}
	if sr.liveCollapse && (name == "write" || name == "edit") {
		// Keep only a bounded excerpt for the compact progress row.
		for _, r := range delta {
			view.compactTail = append(view.compactTail, r)
			if len(view.compactTail) > 96 {
				view.compactTail = view.compactTail[len(view.compactTail)-96:]
			}
		}
		// Never stream a hidden file body: keep one mutable progress row.
		// An unfinished visible line from before Ctrl+O must be terminated
		// before drawing the progress indicator.
		if !view.endsWithNewline {
			fmt.Fprint(sr.w, "\n")
			view.endsWithNewline = true
			view.codeLine.Reset()
			view.codeLineOverflow = false
			sr.requestLiveSyntaxRefreshLocked()
		}
		sr.showCollapsedLiveCallLocked(index, view)
		return
	}
	if view.compactLine != -1 {
		// Expanding during a call resumes new content. The final redraw
		// restores any earlier bytes deliberately hidden by collapse.
		view.compactLine = -1
		sr.requestLiveSyntaxRefreshLocked()
	}
	if delta == "" {
		return
	}
	// The stream collector has already decoded JSON string escapes and
	// filtered terminal control characters. Preserve real newlines here.
	if name == "write" {
		sr.writeLiveCodeLocked(view, delta)
	} else {
		fmt.Fprint(sr.w, delta)
	}
	view.bytes += len(delta)
	view.endsWithNewline = strings.HasSuffix(delta, "\n")
}

// showCollapsedLiveCallLocked updates a single compact progress row, using
// the same label format as ShowToolDraft instead of streaming file content.
// The source still accumulates in the tool sequencer for normal execution.
func (sr *StreamRenderer) showCollapsedLiveCallLocked(index int, view *liveCallView) {
	clean := safeLiveLabel(strings.Join(strings.Fields(string(view.compactTail)), " "))
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
	label := fmt.Sprintf("↳ receiving %s #%d (%d bytes)", view.name, index+1, view.receivedBytes)
	if clean != "" && maxExcerpt > 0 && view.name == "write" {
		label = fmt.Sprintf("↳ receiving write #%d: %s", index+1, clean)
	}
	line := style.NewStyle().Foreground(sr.theme.Border).Render(label)
	if view.compactLine != -1 {
		// If the row scrolled away, leave the earlier progress text alone.
		if view.compactLine >= 0 {
			_ = ReplaceTrackedStreamLine(sr.w, view.compactLine, line)
		}
		return
	}
	view.compactLine = GetNewlineCount(sr.w)
	if view.compactLine < 0 {
		view.compactLine = -2 // Printed once on a non-repaintable writer.
	}
	fmt.Fprintln(sr.w, line)
	view.endsWithNewline = true
}

// writeLiveCodeLocked prints every incoming fragment immediately. Once a
// newline arrives, the completed logical line can be recolored in place.
func (sr *StreamRenderer) writeLiveCodeLocked(view *liveCallView, delta string) {
	const maxTrackedLine = 4096
	for delta != "" {
		end := strings.IndexByte(delta, '\n')
		fragment := delta
		if end >= 0 {
			fragment = delta[:end]
		}
		if fragment != "" {
			fmt.Fprint(sr.w, fragment)
			if !view.codeLineOverflow {
				if view.codeLine.Len()+len(fragment) > maxTrackedLine {
					view.codeLine.Reset()
					view.codeLineOverflow = true
				} else {
					view.codeLine.WriteString(fragment)
				}
			}
		}
		if end < 0 {
			return
		}
		sr.flushLiveCodeLineLocked(view)
		fmt.Fprint(sr.w, "\n")
		delta = delta[end+1:]
	}
}

// flushLiveCodeLineLocked recolors only the current visible row. The plain
// text is already on screen; a failed repaint must never duplicate the body.
func (sr *StreamRenderer) flushLiveCodeLineLocked(view *liveCallView) {
	line := view.codeLine.String()
	overflow := view.codeLineOverflow
	view.codeLine.Reset()
	view.codeLineOverflow = false
	if overflow {
		sr.requestLiveSyntaxRefreshLocked()
		return
	}
	if line == "" {
		return
	}

	width, _ := style.GetTerminalSize()
	if width <= 1 || len(line) >= width {
		sr.requestLiveSyntaxRefreshLocked()
		return
	}
	for _, r := range line {
		// Tabs, control characters and wide glyphs do not have a reliable
		// one-byte-per-cell mapping for single-row terminal replacement.
		if r < ' ' || r > '~' {
			sr.requestLiveSyntaxRefreshLocked()
			return
		}
	}

	// Use the streamed file extension, as in the previously working live
	// write renderer. Do not run a second lexer-resolution path here.
	language := DetectLangFromPath(view.path)
	if language == "plaintext" {
		if view.language == "" {
			view.language = DetectLangFromContent(line)
		}
		if view.language != "" {
			language = view.language
		}
	}
	var colored strings.Builder
	if err := HighlightWithoutTrailingNewline(&colored, line, language, sr.theme.ChromaStyle); err != nil {
		sr.requestLiveSyntaxRefreshLocked()
		return
	}
	// If a destination arrived after the source, the history renderer will
	// have a more reliable lexer than the live preview had at this point.
	if view.path == "" {
		sr.requestLiveSyntaxRefreshLocked()
	}
	if !strings.Contains(colored.String(), "\x1b[") {
		if language != "plaintext" {
			sr.requestLiveSyntaxRefreshLocked()
		}
		return
	}
	// Prefer the direct terminal row repaint. Unlike printing cursor controls
	// through the stream, it preserves the writer's tracking and scroll state.
	for w := sr.w; w != nil; {
		if painter, ok := w.(interface{ RepaintActiveScrollLine(string) bool }); ok {
			if painter.RepaintActiveScrollLine(colored.String()) {
				return
			}
			break
		}
		unwrapper, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == nil || next == w {
			break
		}
		w = next
	}
	// One last tracked-row attempt for non-interactive writers and terminals
	// that temporarily cannot repaint their current row.
	if !sr.trackedRowsInvalidated {
		if position := GetNewlineCount(sr.w); position >= 0 &&
			ReplaceTrackedStreamLine(sr.w, position, colored.String()) {
			return
		}
	}
	sr.requestLiveSyntaxRefreshLocked()
}

// requestLiveSyntaxRefreshLocked does not redraw while a model is streaming;
// doing so would replay incomplete tool arguments. The REPL consumes the flag
// after the turn, when saved history can be rendered correctly.
func (sr *StreamRenderer) requestLiveSyntaxRefreshLocked() {
	for w := sr.w; w != nil; {
		if refresher, ok := w.(interface{ MarkSyntaxRefresh() }); ok {
			refresher.MarkSyntaxRefresh()
			return
		}
		unwrapper, ok := w.(interface{ Unwrap() io.Writer })
		if !ok {
			return
		}
		next := unwrapper.Unwrap()
		if next == nil || next == w {
			return
		}
		w = next
	}
}

// liveToolTitle matches the final tool title's path formatting so previews
// do not alternate between absolute and relative paths during execution.
func (sr *StreamRenderer) liveToolTitle(name, path string) string {
	symbol := RenderToolSymbol(name, ToolStatusPending, sr.theme)
	if name == "bash" {
		return FormatBashCommandLine(symbol, "", sr.theme) + " "
	}
	return FormatToolTitle(symbol, name, path, sr.theme)
}

// BeginToolExecution repeats a heading only if another call's preview now
// occupies the output tail. Execution output stays attached to its own call.
func (sr *StreamRenderer) BeginToolExecution(index int, name, arguments string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	view := sr.liveCallViews[index]
	if view == nil || !view.headerShown || name == "write" || name == "edit" || sr.liveVisibleCall == index {
		return
	}
	sr.finishLiveTextLocked()
	target := ExtractToolTarget(name, arguments)
	view.titleLine = GetNewlineCount(sr.w)
	symbol := RenderToolSymbol(name, ToolStatusPending, sr.theme)
	if name == "bash" {
		fmt.Fprintln(sr.w, FormatBashCommandLine(symbol, target, sr.theme))
	} else {
		fmt.Fprintln(sr.w, FormatToolTitle(symbol, name, safeLiveLabel(target), sr.theme))
	}
	sr.liveVisibleCall = index
}

func (sr *StreamRenderer) EndLiveToolContent() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.liveActiveCall >= 0 {
		if view := sr.liveCallViews[sr.liveActiveCall]; view != nil {
			sr.endLiveCallLocked(sr.liveActiveCall, view)
			sr.liveResultGap = true
		}
	}
	sr.liveActiveCall = -1
}

// endLiveCallLocked reserves a single completion row for a streamed write
// or edit. The engine replaces this row with a verified summary, not a second
// copy of the edit diff. Failed operations still show their full error.
func (sr *StreamRenderer) endLiveCallLocked(index int, view *liveCallView) {
	if view.compactLine != -1 && (view.name == "write" || view.name == "edit") {
		// The compact progress row becomes this call's verified result.
		// Reusing it avoids a spare row between progress and completion,
		// and lets the engine skip its normal inter-tool spacing here.
		if sr.writeResultLines == nil {
			sr.writeResultLines = make(map[int]int)
		}
		sr.writeResultLines[index] = view.compactLine
		return
	}
	if view.name == "write" && view.codeLine.Len() > 0 {
		sr.flushLiveCodeLineLocked(view)
	}
	if !view.endsWithNewline {
		fmt.Fprint(sr.w, "\n")
		view.endsWithNewline = true
	}
	if view.name != "write" && (view.name != "edit" || view.bytes == 0) {
		return
	}
	if sr.writeResultLines == nil {
		sr.writeResultLines = make(map[int]int)
	}
	if line := GetNewlineCount(sr.w); line >= 0 {
		sr.writeResultLines[index] = line
	}
	fmt.Fprint(sr.w, "\n")
}

// RecordToolResult retains the authoritative output of an early call until its
// assistant response and tool result have reached committed history.
func (sr *StreamRenderer) RecordToolResult(index int, arguments, output string, failed bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if view := sr.liveCallViews[index]; view != nil {
		view.arguments = arguments
		view.output = output
		view.failed = failed
		view.completed = true
		view.resultRecorded = true
	}
}
