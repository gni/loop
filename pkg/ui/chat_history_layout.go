package ui

import (
	"fmt"
	"strings"

	"loop/pkg/config"
	"loop/pkg/domain/message"
	"loop/pkg/ui/render"
)

type cachedTurnLayout struct {
	key              string
	rows             *render.Scrollback
	archived, failed bool
}

type turnLayoutJob struct {
	historyPageJob
	turn      *displayTurn
	key       string
	width     int
	alternate *cachedTurnLayout
	cancelled bool
}

func (h *ChatHistory) cachedLayoutReady(turn *displayTurn, key string) bool {
	return turn.alternate != nil && turn.alternate.key == key && turn.alternate.rows.Width == h.buffer.Width
}

// Finished turns are rebuilt ahead of the reader, never under TerminalMu.
// One alternate per turn makes repeated collapse/expand a row-index swap.
func (h *ChatHistory) scheduleLayoutsLocked() {
	key := h.appearanceKey()
	if job := h.layoutJob; job != nil {
		if job.alternate == nil && (job.key != key || job.width != h.buffer.Width) && !job.cancelled {
			close(job.stop)
			job.cancelled = true
		}
		return
	}
	if h.closed.Load() {
		return
	}
	top := h.top
	if !h.browsing {
		top = h.buffer.Base()
	}
	var target *displayTurn
	best := int(^uint(0) >> 1)
	var archiveTurn *displayTurn
	for _, turn := range h.turns {
		if turn.alternate != nil && !turn.alternate.archived && !turn.alternate.failed {
			archiveTurn = turn
		}
		if turn == h.active || turn.appearance == key || turn.failedLayout == key || h.cachedLayoutReady(turn, key) {
			continue
		}
		if len(turn.messages) == 0 && turn.size == 0 {
			turn.appearance = key
			continue
		}
		score := 0
		if turn.end < top {
			score = top - turn.end
		} else if turn.start > top+h.buffer.Height {
			score = turn.start - top - h.buffer.Height
		}
		if (h.scrollDirection <= 0 && turn.start > top+h.buffer.Height) || (h.scrollDirection > 0 && turn.end < top) {
			score += h.buffer.Base() + h.buffer.Height + 1
		}
		if score > h.prefetchMarginLocked() {
			continue
		}
		if score < best {
			target, best = turn, score
		}
	}
	if archiveTurn != nil && (target == nil || best > 0) {
		alt := archiveTurn.alternate
		rows := alt.rows.SnapshotHistory()
		job := &turnLayoutJob{historyPageJob: historyPageJob{stop: make(chan struct{}), file: h.turnFile}, turn: archiveTurn, key: alt.key, width: rows.Width, alternate: alt}
		h.layoutJob = job
		go func() {
			err := rows.ArchiveHistory(job.stop)
			TerminalMu.Lock()
			defer TerminalMu.Unlock()
			h.finishLayoutJobLocked(job, rows, err)
		}()
		return
	}
	if target == nil {
		return
	}
	cfg, err := config.Clone(h.a.Config)
	if err != nil {
		h.historyErr = err
		return
	}
	job := &turnLayoutJob{historyPageJob: historyPageJob{stop: make(chan struct{}), file: h.turnFile}, turn: target, key: key, width: h.buffer.Width}
	h.layoutJob = job
	rows := h.buffer.NewHistoryPage()
	messages := message.CloneAll(target.messages)
	offset, size := target.offset, target.size
	theme := GetConfiguredTheme(cfg)
	go func() {
		var err error
		if len(messages) == 0 && size > 0 {
			messages, err = h.readTurnMessagesFromFile(job.file, offset, size)
		}
		if err == nil {
			hooks := render.HistoryRenderHooks{}
			if !cfg.MarkdownRender {
				hooks.RenderMarkdown = render.RenderRawMarkdownContent
			}
			render.PrintSessionHistory(historyPageWriter{rows, job.stop}, messages, theme, cfg, false, cfg.CollapseResults, hooks)
			rows.FinishTurnLayout()
			err = rows.ArchiveHistory(job.stop)
		}
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		h.finishLayoutJobLocked(job, rows, err)
	}()
}

func (h *ChatHistory) finishLayoutJobLocked(job *turnLayoutJob, rows *render.Scrollback, err error) {
	if h.closed.Load() || h.layoutJob != job {
		_ = rows.Close()
		return
	}
	h.layoutJob = nil
	if job.cancelled {
		_ = rows.Close()
		h.needsPaint = true
		h.queuePaintLocked()
		h.scheduleLayoutsLocked()
		return
	}
	turn := job.turn
	redraw := false
	if err != nil {
		redraw = true
		_ = rows.Close()
		h.historyErr = fmt.Errorf("refresh history layout: %w", err)
		if job.alternate != nil {
			job.alternate.failed = true
		} else {
			turn.failedLayout = job.key
		}
	} else if job.alternate != nil {
		if turn.alternate == job.alternate {
			_ = turn.alternate.rows.Close()
			turn.alternate.rows = rows
			turn.alternate.archived = true
		} else {
			_ = rows.Close()
		}
	} else {
		index := -1
		for i, t := range h.turns {
			if t == turn {
				index = i
				break
			}
		}
		if index >= 0 && turn != h.active && job.key == h.appearanceKey() && job.width == h.buffer.Width && turn.appearance != job.key {
			live := turn.end > h.buffer.Base()
			h.restoreTurnLayoutLocked(index, rows, job.key)
			redraw = h.browsing || live
			if live && h.active == nil {
				h.syncHistoryWriterLocked()
			}
		} else {
			_ = rows.Close()
		}
	}
	if h.layoutScroll != 0 {
		before := h.top
		debt := h.layoutScroll
		h.layoutScroll = 0
		h.top = h.limitLayoutScrollLocked(h.top, min(h.buffer.Base(), max(0, h.top+debt)))
		h.browsing = h.top < h.buffer.Base()
		redraw = redraw || before != h.top
	}
	if redraw {
		h.needsPaint = true
		h.queuePaintLocked()
	}
	h.scheduleLayoutsLocked()
}

func (h *ChatHistory) restoreTurnLayoutLocked(index int, rows *render.Scrollback, key string) {
	turn := h.turns[index]
	oldKey, start, end := turn.appearance, turn.start, turn.end
	if turn.alternate != nil && turn.alternate.rows != rows {
		_ = turn.alternate.rows.Close()
	}
	old, delta, padding := h.buffer.SwapHistoryRange(start, end, rows)
	_ = rows.Close()
	if oldKey != "" {
		turn.alternate = &cachedTurnLayout{key: oldKey, rows: old}
	} else {
		_ = old.Close()
		turn.alternate = nil
	}
	turn.start, turn.end = start+padding, end+delta+padding
	turn.appearance, turn.failedLayout = key, ""
	h.historyStart += padding
	for _, earlier := range h.turns[:index] {
		earlier.start += padding
		earlier.end += padding
	}
	for _, later := range h.turns[index+1:] {
		later.start += delta + padding
		later.end += delta + padding
	}
	if h.searchRow >= end {
		h.searchRow += delta + padding
	} else if h.searchRow >= start {
		h.searchRow = start + padding + min(h.searchRow-start, max(0, turn.end-turn.start-1))
	}
	if h.top >= end {
		h.top += delta + padding
	} else if h.top >= start {
		if h.browsing && h.scrollDirection < 0 && delta > 0 && h.top > start {
			h.top = turn.end - (end - h.top)
		} else {
			h.top = start + padding + min(h.top-start, max(0, turn.end-turn.start-h.buffer.Height))
		}
	} else {
		h.top += padding
	}
	if !h.browsing {
		h.top = h.buffer.Base()
	}
}

func (h *ChatHistory) syncHistoryWriterLocked() {
	transcript := []byte(strings.Join(h.buffer.View(h.buffer.Base()), "\r\n"))
	h.writer.ResetAfterReplayLocked(h.writer.height, transcript)
	if counter, ok := h.a.CurrentWriter().(interface{ ResetAfterReplay([]byte) }); ok {
		counter.ResetAfterReplay(transcript)
	}
}

// Keep scroll intent at a stale block until its background layout arrives.
// Cached blocks pass immediately, and navigation/typing remain responsive.
func (h *ChatHistory) limitLayoutScrollLocked(from, to int) int {
	key := h.appearanceKey()
	limited := to
	for _, turn := range h.turns {
		if turn == h.active || (len(turn.messages) == 0 && turn.size == 0) || turn.appearance == key || turn.failedLayout == key || h.cachedLayoutReady(turn, key) {
			continue
		}
		if to < from {
			if from >= turn.end && to < turn.end {
				limited = max(limited, turn.end)
			} else if from >= turn.start && from < turn.end {
				limited = from
			}
		} else if to > from {
			if from+h.buffer.Height <= turn.start && to+h.buffer.Height > turn.start {
				limited = min(limited, max(from, turn.start-h.buffer.Height))
			} else if turn.end > from && turn.start < from+h.buffer.Height {
				limited = from
			}
		}
	}
	h.layoutScroll += to - limited
	return limited
}
