package ui

import (
	"fmt"
	"strings"
	"unicode"

	"loop/pkg/ui/stream"
)

// liveCallView records what the terminal has already displayed for a call.
// Write and edit arguments can be previewed during generation. Execution
// results for all tools are published by the engine, never by this preview.
type liveCallView struct {
	name             string
	path             string
	titleLine        int
	headerShown      bool
	lastRenderedPath string
	bytes            int
	endsWithNewline  bool
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
// rewriting an arbitrarily sized block of terminal rows. This works even when
// path is delivered after content, and does not reserve blank terminal rows.
func (sr *StreamRenderer) ShowLiveToolContent(index int, name, path, delta string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if name != "write" && name != "edit" {
		return
	}
	if sr.liveCallViews == nil {
		sr.liveCallViews = make(map[int]*liveCallView)
	}
	view := sr.liveCallViews[index]
	if view == nil {
		view = &liveCallView{name: name, titleLine: -1, endsWithNewline: true}
		sr.liveCallViews[index] = view
	}
	path = safeLiveLabel(path)
	if path != "" {
		view.path = path
	}
	if !view.headerShown || sr.liveActiveCall != index {
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
		title := liveToolTitle(name, view.path)
		view.titleLine = stream.GetNewlineCount(sr.w)
		fmt.Fprintln(sr.w, title)
		view.headerShown = true
		view.lastRenderedPath = view.path
		view.endsWithNewline = true
		sr.liveActiveCall = index
	} else if view.path != "" && view.path != view.lastRenderedPath && view.titleLine >= 0 {
		// Updating an existing heading is best effort. If it has scrolled
		// away, leave the original title rather than corrupting another row.
		if stream.ReplaceTrackedStreamLine(sr.w, view.titleLine, liveToolTitle(name, view.path)) {
			view.lastRenderedPath = view.path
		}
	}
	if delta == "" {
		return
	}
	// The stream collector has already decoded JSON string escapes and
	// filtered terminal control characters. Preserve real newlines here.
	fmt.Fprint(sr.w, delta)
	view.bytes += len(delta)
	view.endsWithNewline = strings.HasSuffix(delta, "\n")
}

// EndLiveToolContent leaves the transcript on a complete output row. It does
// not erase generated content or insert placeholder lines; the engine's
// execution results follow after all model calls have finished generating.
func liveToolTitle(name, path string) string {
	symbol := "◆"
	if name == "edit" {
		symbol = "✎"
	}
	if path == "" {
		path = "…"
	}
	return symbol + " " + name + " " + path
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
	if line := stream.GetNewlineCount(sr.w); line >= 0 {
		sr.writeResultLines[index] = line
	}
	fmt.Fprint(sr.w, "\n")
}
