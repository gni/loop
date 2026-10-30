package render

import (
	"fmt"
	"strings"
	"unicode"

	"loop/pkg/ui/style"
)

// The preview is a fixed-height part of the conversation, not the status bar.
// It can be repainted without scrolling every time a new token arrives. After
// inference, the engine renders validated calls and results in execution order.
const livePreviewRows = 8

func (sr *StreamRenderer) CanStreamLiveToolPreview() bool {
	return CanReplaceTrackedStreamLine(sr.w)
}

func cleanPreviewLine(value string, width int) string {
	var result []rune
	escape := 0
	for _, r := range value {
		if escape != 0 {
			switch escape {
			case 1:
				if r == '[' {
					escape = 2
				} else if r == ']' {
					escape = 3
				} else {
					escape = 0
				}
			case 2:
				if r >= 0x40 && r <= 0x7e {
					escape = 0
				}
			case 3:
				if r == '\a' {
					escape = 0
				} else if r == '\\' {
					escape = 0
				}
			}
			continue
		}
		if r == '\x1b' {
			escape = 1
			continue
		}
		if r == '\r' || r == 127 || (unicode.IsControl(r) && r != '\t') {
			continue
		}
		if r == '\t' {
			r = ' '
		}
		result = append(result, r)
		if len(result) >= width {
			break
		}
	}
	return string(result)
}

func (sr *StreamRenderer) ShowToolLivePreview(index int, name, excerpt string, size int) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if !CanReplaceTrackedStreamLine(sr.w) {
		return
	}
	sr.endThinking()
	sr.finishLiveTextLocked()

	width, _ := style.GetTerminalSize()
	if width <= 0 {
		width = 100
	}
	width -= 4
	if width < 20 {
		width = 20
	}

	if name == "" {
		name = "…"
	}
	name = cleanPreviewLine(name, 32)
	title := GetToolGlyph(name) + " " + name
	if !strings.Contains(name, " ") {
		title += " …"
	}
	lines := make([]string, livePreviewRows)
	lines[0] = cleanPreviewLine(title, width)
	// Last lines show live text, not a one-line excerpt. Keeping the tail
	// bounded also avoids unbounded cursor redraws for large file writes.
	body := strings.Split(strings.ReplaceAll(excerpt, "\r", ""), "\n")
	if len(body) > livePreviewRows-1 {
		body = body[len(body)-(livePreviewRows-1):]
	}
	for i, line := range body {
		lines[i+1] = cleanPreviewLine(line, width)
	}
	snapshot := strings.Join(lines, "\n")
	if sr.livePreviewVisible && sr.livePreviewSnapshot == snapshot {
		return
	}
	if !sr.livePreviewVisible {
		position := GetNewlineCount(sr.w)
		if position < 0 {
			return
		}
		sr.livePreviewLine = position
		for _, line := range lines {
			fmt.Fprintln(sr.w, line)
		}
		sr.livePreviewVisible = true
		sr.livePreviewSnapshot = snapshot
		return
	}
	if ReplaceTrackedStreamBlock(sr.w, sr.livePreviewLine, lines) {
		sr.livePreviewSnapshot = snapshot
	}
}

func (sr *StreamRenderer) ClearToolLivePreview() {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if !sr.livePreviewVisible {
		return
	}
	lines := make([]string, livePreviewRows)
	ReplaceTrackedStreamBlock(sr.w, sr.livePreviewLine, lines)
	sr.livePreviewLine = -1
	sr.livePreviewVisible = false
	sr.livePreviewSnapshot = ""
}
