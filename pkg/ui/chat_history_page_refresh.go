package ui

import (
	"io"

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
	p := &preparedHistoryPage{rows: rows, more: old.more, appearance: key}
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
		turn := &displayTurn{start: 0, end: max(1, count-1), offset: previous.offset, size: previous.size, messages: previous.messages, appearance: key}
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
		_ = p.rows.Close()
		return
	}
	h.pageJob = nil
	if p.err != nil {
		h.historyErr = p.err
		if p.more && source != nil {
			_ = source.Close()
		}
		p.more = false
	}
	if !p.more {
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
	applied := false
	if h.readyPage != nil && h.browsing && (h.pendingHome || h.top <= h.historyStart+h.prefetchMarginLocked()) {
		applied = h.applyOlderPageLocked() > 0
	}
	if applied || p.err != nil || (!p.more && h.browsing) {
		h.needsPaint = true
		h.queuePaintLocked()
	}
}
