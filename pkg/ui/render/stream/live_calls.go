package stream

import (
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"loop/pkg/ui/render"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/render/syntax"
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
	bashLineIndex    int
	bashLineStarted  bool
	bashHighlighter  *syntax.BashCommandHighlighter
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

// safeLiveLabel strips complete ANSI escape sequences and control characters
// from model-supplied labels while keeping grapheme clusters intact.
func safeLiveLabel(value string) string {
	value = style.StripAnsi(strings.ToValidUTF8(value, ""))
	var b strings.Builder
	b.Grow(min(len(value), 240))
	cells := 0
	for i := 0; i < len(value); {
		cluster, cw, sz := style.NextGrapheme(value[i:])
		i += sz
		first, _ := utf8.DecodeRuneInString(cluster)
		if unicode.IsControl(first) {
			continue
		}
		if cells+cw > 240 {
			break
		}
		b.WriteString(cluster)
		cells += cw
	}
	return strings.TrimSpace(b.String())
}

// TrackProgressCall records a quiet call even when argument previews are off.
// Its result may still contain a blocker that must survive a redraw.
func (sr *StreamRenderer) TrackProgressCall(index int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.trackProgressCallLocked(index)
}

func (sr *StreamRenderer) trackProgressCallLocked(index int) {
	if sr.liveCallViews == nil {
		sr.liveCallViews = make(map[int]*liveCallView)
	}
	if sr.liveCallViews[index] != nil {
		return
	}
	sr.liveCallViews[index] = &liveCallView{name: "task_state", titleLine: -1, compactLine: -1, endsWithNewline: true}
	sr.liveReplayEvents = append(sr.liveReplayEvents, liveReplayEvent{kind: "tool", index: index})
}

// ShowLiveToolContent is called in model-stream order. It never writes a file
// and never runs a tool. It appends actual decoded text deltas, rather than
// rewriting an arbitrarily sized block of terminal rows. Writes stream even
// before their filename arrives; edits retain their target-based diff preview.
func (sr *StreamRenderer) ShowLiveToolContent(index int, name, path, delta string) {
	name = safeLiveLabel(name)
	if name == "" {
		return
	}
	if name != "task_state" {
		sr.runBeforeWrite()
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if name == "task_state" {
		sr.trackProgressCallLocked(index)
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
	rawPath := path
	path = safeLiveLabel(path)
	if path != "" {
		view.path = path
	}
	// A call whose preview was closed by early execution or a later call
	// must not reopen when the provider sends trailing argument fragments.
	if view.headerShown && sr.liveActiveCall != index {
		return
	}
	if name == "bash" && delta == "" && rawPath != "" && view.fullContent.Len() == 0 {
		delta = style.StripAnsi(strings.ToValidUTF8(rawPath, ""))
	}
	view.receivedBytes += len(delta)
	view.fullContent.WriteString(delta)
	if !view.headerShown && (name == "write" || name == "edit") {
		if view.path == "" && (name == "edit" || view.fullContent.Len() == 0) {
			return
		}
		// A write can start with an unnamed heading as soon as content
		// arrives. Keep its eventual filename in this same call instead of
		// appending a second heading below the source.
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
		view.endsWithNewline = strings.HasSuffix(delta, "\n")
	} else if name == "bash" {
		sr.writeLiveBashLocked(view, delta)
	} else {
		fmt.Fprint(sr.w, delta)
		view.endsWithNewline = strings.HasSuffix(delta, "\n")
	}
	view.bytes += len(delta)
}

// showCollapsedLiveCallLocked updates a single compact progress row instead of
// streaming file content. The source still accumulates in the tool sequencer.
func (sr *StreamRenderer) showCollapsedLiveCallLocked(index int, view *liveCallView) {
	clean := safeLiveLabel(strings.Join(strings.Fields(string(view.compactTail)), " "))
	maxExcerpt := 72
	if width := markdown.RenderWidth(sr.w); width > 0 && width < 100 {
		maxExcerpt = max(0, width-28)
	}
	clean = render.TailCells(clean, maxExcerpt)
	label := fmt.Sprintf("↳ receiving %s #%d (%d bytes)", view.name, index+1, view.receivedBytes)
	if clean != "" && maxExcerpt > 0 && view.name == "write" {
		label = fmt.Sprintf("↳ receiving write #%d: %s", index+1, clean)
	}
	line := style.NewStyle().Foreground(sr.theme.TextMuted).Render(label)
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

// writeLiveBashLocked streams every incoming bash command fragment immediately
// as it arrives, then recolors each completed line in place (including embedded
// heredocs like `python3 - <<'PY'`) once a newline or tool completion arrives.
func (sr *StreamRenderer) writeLiveBashLocked(view *liveCallView, delta string) {
	const maxTrackedBashLine = 16384
	delta = strings.ReplaceAll(delta, "\r\n", "\n")
	delta = strings.ReplaceAll(delta, "\r", "\n")
	textStyle := style.NewStyle().Foreground(sr.theme.Text)
	for delta != "" {
		end := strings.IndexByte(delta, '\n')
		fragment := delta
		if end >= 0 {
			fragment = delta[:end]
		}
		if view.bashLineIndex == 0 && !view.bashLineStarted && !view.codeLineOverflow &&
			strings.TrimSpace(view.codeLine.String()) == "" && strings.TrimSpace(fragment) == "" {
			if end >= 0 {
				view.codeLine.Reset()
				delta = delta[end+1:]
				continue
			}
			view.codeLine.WriteString(fragment)
			view.endsWithNewline = false
			return
		}
		if fragment != "" {
			cleaned := cleanBashCommandChunk(fragment)
			if cleaned != "" {
				if !view.bashLineStarted {
					if view.bashLineIndex > 0 {
						fmt.Fprint(sr.w, "  ")
					}
					if pending := cleanBashCommandChunk(view.codeLine.String()); pending != "" {
						fmt.Fprint(sr.w, textStyle.Render(pending))
					}
					view.bashLineStarted = true
				}
				fmt.Fprint(sr.w, textStyle.Render(cleaned))
				if !view.codeLineOverflow {
					if view.codeLine.Len()+len(cleaned) > maxTrackedBashLine {
						view.codeLine.Reset()
						view.codeLineOverflow = true
					} else {
						view.codeLine.WriteString(cleaned)
					}
				}
			}
		}
		if end < 0 {
			view.endsWithNewline = false
			return
		}
		sr.flushLiveBashLineLocked(view, true)
		delta = delta[end+1:]
	}
}

func (sr *StreamRenderer) flushLiveBashLineLocked(view *liveCallView, withNewline bool) {
	line := view.codeLine.String()
	overflow := view.codeLineOverflow
	lineIdx := view.bashLineIndex
	view.codeLine.Reset()
	view.codeLineOverflow = false
	view.bashLineStarted = false
	view.bashLineIndex++

	defer func() {
		if withNewline {
			fmt.Fprint(sr.w, "\n")
			view.endsWithNewline = true
		} else {
			view.endsWithNewline = false
		}
	}()

	if !sr.bashSyntaxColor {
		return
	}
	if overflow {
		sr.requestLiveSyntaxRefreshLocked()
		return
	}
	if view.bashHighlighter == nil {
		view.bashHighlighter = syntax.NewBashCommandHighlighter(sr.theme)
	}
	styled := view.bashHighlighter.HighlightLine(line)
	if line == "" {
		return
	}
	if !strings.Contains(styled, "\x1b[") {
		return
	}

	width := markdown.RenderWidth(sr.w)
	if width <= 2 {
		sr.requestLiveSyntaxRefreshLocked()
		return
	}
	for _, r := range line {
		if r < ' ' || r > '~' {
			sr.requestLiveSyntaxRefreshLocked()
			return
		}
	}

	var fullRow string
	if lineIdx == 0 {
		fullRow = sr.liveToolTitle("bash", "") + styled
	} else {
		fullRow = "  " + styled
	}

	totalCells := 2 + len(line)
	if totalCells < width {
		for w := sr.w; w != nil; {
			if painter, ok := w.(interface{ RepaintActiveScrollLine(string) bool }); ok {
				if painter.RepaintActiveScrollLine(fullRow) {
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
		if !sr.trackedRowsInvalidated {
			if position := GetNewlineCount(sr.w); position >= 0 &&
				ReplaceTrackedStreamLine(sr.w, position, fullRow) {
				return
			}
		}
		sr.requestLiveSyntaxRefreshLocked()
		return
	}

	wrapped := style.HardWrapAnsiLine(fullRow, width)
	expectedRows := (totalCells + width - 1) / width
	if len(wrapped) == expectedRows {
		for w := sr.w; w != nil; {
			if painter, ok := w.(interface{ ReplaceScrollBlockBack(int, []string) bool }); ok {
				if painter.ReplaceScrollBlockBack(len(wrapped)-1, wrapped) {
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
	}
	sr.requestLiveSyntaxRefreshLocked()
}

func (sr *StreamRenderer) resyncLiveBashStateLocked(view *liveCallView) {
	content := strings.ReplaceAll(strings.ReplaceAll(view.fullContent.String(), "\r\n", "\n"), "\r", "\n")
	view.bytes = view.receivedBytes
	view.bashLineIndex = 0
	view.bashLineStarted = false
	view.codeLine.Reset()
	view.codeLineOverflow = false
	if sr.bashSyntaxColor {
		view.bashHighlighter = syntax.NewBashCommandHighlighter(sr.theme)
	} else {
		view.bashHighlighter = nil
	}
	if content == "" {
		view.endsWithNewline = false
		return
	}
	hasTrailingNL := strings.HasSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		view.endsWithNewline = false
		return
	}
	completedLines := lines
	if hasTrailingNL {
		completedLines = lines[:len(lines)-1]
	} else if len(lines) > 0 {
		completedLines = lines[:len(lines)-1]
		// The final partial line was already painted by ReplayActiveTurn.
		view.codeLineOverflow = true
		view.bashLineStarted = true
	}
	for _, line := range completedLines {
		cleaned := cleanBashCommandChunk(line)
		if view.bashHighlighter != nil {
			_ = view.bashHighlighter.HighlightLine(cleaned)
		}
		view.bashLineIndex++
	}
	view.endsWithNewline = hasTrailingNL
}

func cleanBashCommandChunk(s string) string {
	return strings.Map(func(r rune) rune {
		if (r < 32 && r != '\t') || r == 127 {
			return -1
		}
		return r
	}, s)
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

	width := markdown.RenderWidth(sr.w)
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
	if err := syntax.HighlightWithoutTrailingNewline(&colored, line, language, sr.theme.ChromaStyle); err != nil {
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
	symbol := render.RenderToolSymbol(name, render.ToolStatusPending, sr.theme)
	if name == "bash" {
		return render.FormatBashCommandLine(symbol, "", sr.theme) + " "
	}
	return render.FormatToolTitleForWidth(symbol, name, path, sr.theme, markdown.RenderWidth(sr.w))
}

// BeginToolExecution repeats a heading only if another call's preview now
// occupies the output tail. Execution output stays attached to its own call.
func (sr *StreamRenderer) BeginToolExecution(index int, name, arguments string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	view := sr.liveCallViews[index]
	if view == nil || !view.headerShown || name == "write" || name == "edit" {
		return
	}
	if sr.liveVisibleCall == index {
		if view.lastRenderedPath == "" && view.titleLine >= 0 && name != "bash" && name != "ask_user" {
			if target := safeLiveLabel(render.ExtractToolTarget(name, arguments)); target != "" {
				symbol := render.RenderToolSymbol(name, render.ToolStatusPending, sr.theme)
				if ReplaceTrackedStreamLine(sr.w, view.titleLine, render.FormatToolTitleForWidth(symbol, name, target, sr.theme, markdown.RenderWidth(sr.w))) {
					view.path = target
					view.lastRenderedPath = target
				}
			}
		}
		return
	}
	sr.finishLiveTextLocked()
	target := render.ExtractToolTarget(name, arguments)
	view.titleLine = GetNewlineCount(sr.w)
	symbol := render.RenderToolSymbol(name, render.ToolStatusPending, sr.theme)
	if name == "bash" {
		fmt.Fprintln(sr.w, render.FormatBashCommandLineStyled(symbol, target, sr.theme, sr.bashSyntaxColor))
	} else {
		fmt.Fprintln(sr.w, render.FormatToolTitleForWidth(symbol, name, safeLiveLabel(target), sr.theme, markdown.RenderWidth(sr.w)))
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
	if view.name == "bash" && (view.bashLineStarted || view.codeLineOverflow) {
		sr.flushLiveBashLineLocked(view, false)
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
