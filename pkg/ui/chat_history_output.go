package ui

import (
	"io"
	"strings"

	"loop/pkg/ui/style"
)

type historyOutput struct {
	destination io.Writer
	history     *ChatHistory
}

func (w *historyOutput) Write(data []byte) (int, error) {
	h := w.history
	if !h.captureOnly && (h.initialLoading || h.initialFilling) && strings.TrimSpace(style.StripAnsi(string(data))) != "" {
		h.initialOutput = true
		h.initialFilling = false
	}
	if !h.captureOnly {
		h.clearChatLogoLocked()
	}
	dropped := h.buffer.Dropped()
	h.buffer.Write(data)
	if err := h.buffer.Err(); err != nil {
		h.historyErr = err
	}
	if delta := h.buffer.Dropped() - dropped; delta > 0 {
		h.top = max(0, h.top-delta)
		h.historyStart = max(0, h.historyStart-delta)
		if h.searchRow >= 0 {
			h.searchRow = max(0, h.searchRow-delta)
		}
		if h.searchScan != nil {
			h.searchScan.next = max(0, h.searchScan.next-delta)
		}
		for _, turn := range h.turns {
			turn.start = max(0, turn.start-delta)
			turn.end = max(0, turn.end-delta)
		}
	}
	if h.active != nil {
		h.active.end = h.buffer.Base() + h.writer.printLine
	}
	if h.captureOnly {
		return len(data), nil
	}
	if h.browsing {
		h.queuePaintLocked()
		return len(data), nil
	}
	h.drawn = nil
	return w.destination.Write(data)
}
