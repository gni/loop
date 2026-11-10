package render

import (
	"fmt"
	"strings"
	"time"

	"loop/pkg/ui/style"
)

func (sr *StreamRenderer) getThinkingStyle() style.Style {
	border := sr.theme.Border
	if border == nil {
		border = style.Color("#4C566A")
	}
	return style.NewStyle().Foreground(border).Italic(true)
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

func (sr *StreamRenderer) GetReasoningDuration() float64 {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.reasoningDuration
}
