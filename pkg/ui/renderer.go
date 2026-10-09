package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/ui/render"
	"loop/pkg/ui/stream"
	"loop/pkg/ui/style"
)


type StreamRenderer struct {
	mu                        sync.Mutex
	w                         io.Writer
	theme                     UITheme
	inCodeBlock               bool
	inThinking                bool
	showThinking              bool
	reasoningStart            time.Time
	reasoningHasText          bool
	reasoningEndedWithNewline bool
	reasoningDuration         float64
	reasoningResetSequence    string
	pendingThoughtTextGap     bool
	prompt                    string
	echoFilter                *agent.PromptEchoFilter

	lastEndedWithNewline bool
	hasWrittenText       bool
	hasWrittenThoughts   bool
	parser               *stream.JSONStreamParser
	live                 *stream.LiveMarkdownRenderer
}

func NewStreamRenderer(w io.Writer, theme UITheme, showThinking bool, streamWrites bool, _ string) *StreamRenderer {
	sr := &StreamRenderer{
		w:                    w,
		theme:                theme,
		showThinking:         showThinking,
		lastEndedWithNewline: true,
		live:                 stream.NewLiveMarkdownRenderer(w, theme),
	}
	sr.parser = stream.NewJSONStreamParser(streamWrites)
	return sr
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

	dimStyle := sr.getThinkingStyle()
	startSeq, resetSeq := dimStyle.GetSequence()

	if !sr.inThinking {
		sr.inThinking = true
		sr.reasoningStart = time.Now()
		sr.reasoningHasText = false
		sr.reasoningEndedWithNewline = false
		sr.pendingThoughtTextGap = false
		sr.reasoningResetSequence = resetSeq
	}

	sr.hasWrittenThoughts = true
	sr.reasoningHasText = true
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
	labelStyle := style.NewStyle().Foreground(sr.theme.Border).Italic(true)

	fmt.Fprintf(sr.w, "%s\n",
		labelStyle.Render(fmt.Sprintf("thought (%.1fs)", elapsed)),
	)
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

	if chunk == "" {
		return
	}

	if sr.pendingThoughtTextGap {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	sr.checkFirstWrite()
	sr.hasWrittenText = true
	sr.live.Write(chunk)
	sr.lastEndedWithNewline = sr.live.EndedWithNewline()
}

func (sr *StreamRenderer) finishLiveTextLocked() {
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
	sr.endThinking()
	if sr.pendingThoughtTextGap && sr.parser != nil && sr.parser.ActiveToolName != "" {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	sr.pendingThoughtTextGap = false
	sr.finishLiveTextLocked()
	sr.flushActiveToolLocked()
}

func (sr *StreamRenderer) flushActiveToolLocked() {
	if sr.parser != nil && sr.parser.ActiveToolName != "" {
		if !sr.parser.TitlePrinted {
			if sr.parser.ActiveToolName != "bash" || strings.TrimSpace(sr.parser.Path) != "" {
				sr.parser.PrintStreamTitle(sr.w, sr.theme)
			}
		}
		sr.parser.FlushOutputBuf(sr.w, sr.theme)
		if sr.parser.LineBuffer.Len() > 0 {
			sr.parser.EmitLine(sr.w, sr.theme)
		}
	}
}

func (sr *StreamRenderer) printNormalLine(line string) {
	render.PrintNormalMarkdownLine(sr.w, line, sr.theme)
}

func (sr *StreamRenderer) renderInlineMarkdown(text string) string {
	return render.RenderInlineMarkdown(text, sr.inThinking, sr.theme)
}

func HighlightWithoutTrailingNewline(w io.Writer, source, lang, chromaStyle string) error {
	return render.HighlightWithoutTrailingNewline(w, source, lang, chromaStyle)
}

func (sr *StreamRenderer) StartToolCall(toolName string, toolCallIndex int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	sr.endThinking()
	if sr.pendingThoughtTextGap {
		fmt.Fprint(sr.w, "\n")
		sr.pendingThoughtTextGap = false
	}
	hadText := sr.hasWrittenText
	sr.finishLiveTextLocked()
	if hadText {
		fmt.Fprint(sr.w, "\n")
		sr.hasWrittenText = false
	}

	if sr.parser != nil {
		if sr.parser.ActiveToolName != "" && sr.parser.ActiveToolIndex == toolCallIndex {
			sr.parser.ActiveToolName = toolName
			return
		}
		if sr.parser.ActiveToolName != "" {
			sr.flushActiveToolLocked()
		}
		sr.parser.ActiveToolIndex = toolCallIndex
		sr.parser.EnsureTrackingIndex()
		sr.parser.ToolTitleLineNumbers[toolCallIndex] = -1
		sr.parser.ToolBodyStreamed[toolCallIndex] = false
		sr.parser.ActiveToolName = toolName
		sr.parser.TitlePrinted = false
		sr.parser.Path = ""
		sr.parser.PathPrinted = false
		sr.parser.IsContent = false
		sr.parser.IsPath = false
		sr.parser.OutputBuf.Reset()
		sr.parser.LineBuffer.Reset()
		sr.parser.InString = false
		sr.parser.InEscape = false
		sr.parser.CurrentKey = ""
		sr.parser.InValue = false
		sr.parser.Buf.Reset()

		if sr.parser.StreamWrites && !sr.parser.NeedsPath() {
			sr.parser.PrintStreamTitle(sr.w, sr.theme)
		}
	}
}

func (sr *StreamRenderer) WriteToolCall(content string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if len(content) > 0 {
		sr.checkFirstWrite()
	}

	if sr.parser != nil {
		sr.parser.Feed(content, sr.w, sr.theme)
	}
}

func (sr *StreamRenderer) checkFirstWrite() {
	if getUI().PasteLinesOffset > 0 {
		getUI().PasteLinesOffset = 0
		InitStatusBar(os.Stderr)
	}
}

func (sr *StreamRenderer) GetToolTitleLineNumber(index int) int {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.ToolTitleLineNumbers) {
		return -1
	}
	return sr.parser.ToolTitleLineNumbers[index]
}

func (sr *StreamRenderer) DidStreamToolBody(index int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	return sr.parser != nil && index >= 0 && index < len(sr.parser.ToolBodyStreamed) && sr.parser.ToolBodyStreamed[index]
}

func (sr *StreamRenderer) CompleteToolCall(index int, toolName string, toolArgs string, isError bool) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.parser == nil || index < 0 || index >= len(sr.parser.ToolTitleLineNumbers) {
		return
	}
	if sr.parser.ToolTitleLineNumbers[index] < 0 {
		return
	}
	target := extractToolTarget(toolName, toolArgs)
	if target == "" && sr.parser.Path != "" {
		target = sr.parser.Path
	}
	if strings.TrimSpace(target) == "" {
		return
	}
	status := toolStatusSuccess
	if isError {
		status = toolStatusError
	}
	symbol := renderToolSymbol(toolName, status, sr.theme)
	if toolName == "bash" {
		stream.ReplaceTrackedStreamLine(sr.w, sr.parser.ToolTitleLineNumbers[index], FormatBashCommandLine(symbol, target, sr.theme))
		return
	}
	stream.ReplaceTrackedStreamLine(sr.w, sr.parser.ToolTitleLineNumbers[index], FormatToolTitle(symbol, toolName, target, sr.theme))
}

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}
