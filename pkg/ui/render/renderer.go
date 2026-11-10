package render

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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

func (sr *StreamRenderer) checkFirstWrite() {
	if sr.beforeWrite != nil {
		sr.beforeWrite()
	}
}
