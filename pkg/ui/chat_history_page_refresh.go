package ui

import (
	"io"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// A page may finish after collapse/expand changes, or sit prefetched through
// several toggles. Refresh its saved turns before publishing any of its rows.
func (h *ChatHistory) refreshStalePageLocked() {
	p := h.readyPage
	if p == nil || h.pageJob != nil || p.err != nil || p.appearance == h.appearanceKey() {
		return
	}
	cfg, err := config.Clone(h.a.Config)
	if err != nil {
		h.historyErr = err
		return
	}
	job := &historyPageJob{stop: make(chan struct{}), file: h.turnFile}
	h.pageJob, h.readyPage = job, nil
	source := h.source
	rows := h.buffer.NewHistoryPage()
	key, theme := h.appearanceKey(), GetConfiguredTheme(cfg)
	go func() {
		prepared := h.refreshHistoryPage(job, p, rows, cfg, theme, key)
		_ = p.rows.Close()
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		h.finishPreparedPageLocked(job, source, prepared)
	}()
}

func (h *ChatHistory) refreshHistoryPage(job *historyPageJob, old *preparedHistoryPage, rows *render.Scrollback, cfg *config.Config, theme style.UITheme, key string) *preparedHistoryPage {
	p := &preparedHistoryPage{rows: rows, more: old.more, appearance: key, pending: old.pending, pendingTurn: old.pendingTurn}
	seed := rows.PopTailRows(1)
	if seed != nil {
		_ = seed.Close()
	}
	hooks := render.HistoryRenderHooks{}
	if !cfg.MarkdownRender {
		hooks.RenderMarkdown = render.RenderRawMarkdownContent
	}
	for i := len(old.turns) - 1; i >= 0; i-- {
		select {
		case <-job.stop:
			p.err = io.ErrClosedPipe
		default:
		}
		if p.err != nil {
			break
		}
		previous := old.turns[i]
		if previous.fragment != nil || (previous.messages == nil && previous.size == 0) {
			chunk := old.rows.CopyHistoryRange(previous.start, previous.end)
			count := rows.InsertPrepared(0, chunk)
			_ = chunk.Close()
			for _, turn := range p.turns {
				turn.start += count
				turn.end += count
			}
			turn := *previous
			turn.start, turn.end = 0, count
			p.turns = append([]*displayTurn{&turn}, p.turns...)
			continue
		}
		messages := previous.messages
		if messages == nil && previous.size > 0 {
			decoded, readErr := h.readTurnMessagesFromFile(job.file, previous.offset, previous.size)
			if readErr != nil {
				p.err = readErr
				break
			}
			messages = decoded
		}
		chunk := rows.NewHistoryPage()
		render.PrintSessionHistory(historyPageWriter{chunk, job.stop}, messages, theme, cfg, false, cfg.CollapseResults, hooks)
		chunk.FinishHistoryPage()
		if err := chunk.Err(); err != nil {
			p.err = err
			_ = chunk.Close()
			break
		}
		count := rows.InsertPrepared(0, chunk)
		_ = chunk.Close()
		for _, turn := range p.turns {
			turn.start += count
			turn.end += count
		}
		turn := &displayTurn{start: 0, end: count, offset: previous.offset, size: previous.size, messages: previous.messages, appearance: key}
		p.turns = append([]*displayTurn{turn}, p.turns...)
	}
	select {
	case <-job.stop:
		p.err = io.ErrClosedPipe
	default:
	}
	return p
}

func (h *ChatHistory) finishPreparedPageLocked(job *historyPageJob, source displayHistorySource, p *preparedHistoryPage) {
	if h.closed.Load() || h.pageJob != job {
		if p.more && source != nil {
			_ = source.Close()
		}
		if p.pending != nil {
			_ = p.pending.Close()
		}
		_ = p.rows.Close()
		return
	}
	h.pageJob = nil
	h.pendingOlderRows = p.pending
	h.pendingOlderTurn = p.pendingTurn
	if p.err != nil {
		h.historyErr = p.err
		if p.more && source != nil {
			_ = source.Close()
		}
		if h.pendingOlderRows != nil {
			_ = h.pendingOlderRows.Close()
			h.pendingOlderRows = nil
		}
		h.pendingOlderTurn = nil
		p.more = false
	}
	if !p.more && h.pendingOlderRows == nil {
		h.source = nil
	}
	if len(p.turns) == 0 {
		_ = p.rows.Close()
	} else {
		h.readyPage = p
	}
	if p.err == nil && p.appearance != h.appearanceKey() {
		h.refreshStalePageLocked()
		return
	}
	if h.initialLoading {
		h.initialLoading = false
		h.initialFilling = !h.initialOutput && !h.searchPending
		added := h.applyOlderPageLocked()
		h.initialRows = added
		if added > 0 && !h.initialOutput {
			end := h.historyStart + added
			dropped := h.buffer.FinishRestore(end)
			h.historyStart -= dropped
			h.top = max(0, h.top-dropped)
			if h.searchRow >= 0 {
				h.searchRow -= dropped
			}
			for _, turn := range h.turns {
				turn.start -= dropped
				turn.end -= dropped
			}
			end -= dropped
			transcript := []byte(strings.Join(h.buffer.View(h.buffer.Base()), "\r\n"))
			if end <= h.buffer.Height {
				transcript = []byte(strings.Join(h.buffer.View(0)[:end], "\r\n"))
			}
			h.writer.ResetAfterReplayLocked(h.writer.height, transcript)
		}
		h.initialFilling = added > 0 && added < h.buffer.Height && !h.initialOutput && !h.searchPending && h.hasOlderLocked()
		if !h.browsing {
			h.top = h.buffer.Base()
		}
		if h.searchPending && p.err == nil {
			h.continueSearchLocked()
		} else {
			h.searchPending = false
			h.searchScan = nil
		}
		h.needsPaint = true
		h.queuePaintLocked()
		return
	}
	if h.initialFilling {
		if h.readyPage != nil {
			h.initialRows += h.applyOlderPageLocked()
			if !h.browsing {
				h.top = h.buffer.Base()
			}
			h.needsPaint = true
			h.queuePaintLocked()
		}
		h.initialFilling = h.initialRows < h.buffer.Height && !h.initialOutput && !h.searchPending && h.hasOlderLocked()
	}
	if h.searchPending && p.err != nil {
		h.searchPending = false
		h.searchScan = nil
	}
	if h.searchPending && p.err == nil {
		h.continueSearchLocked()
		return
	}
	applied := false
	if h.readyPage != nil && h.browsing && (h.pendingHome || h.top <= h.historyStart+h.prefetchMarginLocked()) {
		applied = h.applyOlderPageLocked() > 0
	}
	if applied || p.err != nil || (!p.more && h.browsing) {
		h.needsPaint = true
		h.queuePaintLocked()
	}
}
