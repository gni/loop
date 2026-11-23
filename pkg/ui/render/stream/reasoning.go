package stream

import (
	"fmt"
	"strings"
	"time"

	"loop/pkg/ui/render"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/style"
)

type discardSizedWriter struct {
	width int
}

func (d discardSizedWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d discardSizedWriter) MarkdownWidth() int          { return d.width }

func (sr *StreamRenderer) rehydrateLiveThinkingLocked() {
	rebuilt := markdown.NewLiveThinkingMarkdownRenderer(discardSizedWriter{width: markdown.RenderWidth(sr.w)}, sr.theme)
	if len(sr.liveReplayEvents) > 0 {
		last := sr.liveReplayEvents[len(sr.liveReplayEvents)-1]
		if last.kind == "reasoning" {
			rebuilt.Write(last.text)
		}
	}
	rebuilt.SetWriter(sr.w)
	sr.liveThinking = rebuilt
	sr.reasoningEndedWithNewline = sr.liveThinking.EndedWithNewline()
}

func (sr *StreamRenderer) WriteReasoning(chunk string) {
	if !sr.showThinking || chunk == "" {
		return
	}
	if strings.TrimSpace(chunk) != "" {
		sr.runBeforeWrite()
	}
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if sr.echoFilter != nil {
		chunk = sr.echoFilter.Write(chunk)
		if chunk == "" {
			return
		}
	}

	if !sr.reasoningHasText {
		if strings.TrimSpace(chunk) == "" {
			return
		}
		chunk = strings.TrimLeft(chunk, "\r\n")
		if chunk == "" {
			return
		}
	}

	sr.writeReasoningChunk(chunk)
}

func (sr *StreamRenderer) writeReasoningChunk(chunk string) {
	if sr.liveActiveCall >= 0 {
		if view := sr.liveCallViews[sr.liveActiveCall]; view != nil {
			sr.endLiveCallLocked(sr.liveActiveCall, view)
			fmt.Fprint(sr.w, "\n")
		}
		sr.liveActiveCall = -1
	}

	dimStyle := markdown.GetThinkingStyle(sr.theme)
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
		sr.liveThinking.ResetAtLineStart()
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
			fmt.Fprintln(sr.w, render.FormatThinkingSummary(sr.theme, 0, true, sr.reasoningLineCount, sr.showMicroseconds))
			sr.reasoningCollapsed = true
		} else if sr.reasoningLineCount != sr.reasoningSummaryLines {
			if !ReplaceTrackedStreamLine(sr.w, sr.reasoningSummaryLine, render.FormatThinkingSummary(sr.theme, 0, true, sr.reasoningLineCount, sr.showMicroseconds)) {
				sr.requestLiveSyntaxRefreshLocked()
			}
		}
		sr.reasoningSummaryLines = sr.reasoningLineCount
		sr.reasoningEndedWithNewline = true
		return
	}
	if sr.markdownEnabled {
		sr.liveThinking.Write(chunk)
		sr.reasoningEndedWithNewline = sr.liveThinking.EndedWithNewline()
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
			if !sr.reasoningHasText {
				rem = strings.TrimLeft(rem, "\r\n")
			}
			if rem != "" {
				sr.writeReasoningChunk(rem)
			}
		}
	}

	if !sr.inThinking {
		return
	}
	sr.inThinking = false

	if !sr.reasoningHasText {
		sr.reasoningResetSequence = ""
		sr.liveThinking.ResetAtLineStart()
		return
	}

	if !sr.reasoningCollapsed {
		if sr.markdownEnabled {
			sr.liveThinking.EnsureTrailingNewline()
			sr.reasoningEndedWithNewline = true
			sr.reasoningResetSequence = ""
		} else {
			if sr.reasoningResetSequence != "" {
				fmt.Fprint(sr.w, sr.reasoningResetSequence)
				sr.reasoningResetSequence = ""
			}
			if !sr.reasoningEndedWithNewline {
				fmt.Fprint(sr.w, "\n")
			}
		}
	} else {
		sr.reasoningResetSequence = ""
	}
	sr.liveThinking.ResetAtLineStart()

	elapsed := time.Since(sr.reasoningStart).Seconds()
	sr.reasoningDuration = elapsed
	for i := len(sr.liveReplayEvents) - 1; i >= 0; i-- {
		if sr.liveReplayEvents[i].kind == "reasoning" {
			sr.liveReplayEvents[i].duration = elapsed
			break
		}
	}
	labelStyle := style.NewStyle().Foreground(sr.theme.TextMuted).Italic(true)

	if sr.reasoningCollapsed {
		if !ReplaceTrackedStreamLine(sr.w, sr.reasoningSummaryLine, render.FormatThinkingSummary(sr.theme, elapsed, false, sr.reasoningLineCount, sr.showMicroseconds)) {
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

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}
