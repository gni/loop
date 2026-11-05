package ui

import (
	"errors"
	"io"
	"os"

	"loop/pkg/config"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

type historyPageJob struct {
	stop chan struct{}
	file *os.File
}

type preparedHistoryPage struct {
	rows       *render.Scrollback
	appearance string
	turns      []*displayTurn
	more       bool
	err        error
	pending    *render.Scrollback
}

type historyPageWriter struct {
	rows *render.Scrollback
	stop <-chan struct{}
}

func (w historyPageWriter) Write(data []byte) (int, error) {
	select {
	case <-w.stop:
		return 0, io.ErrClosedPipe
	default:
		return w.rows.Write(data)
	}
}

func (h *ChatHistory) hasOlderLocked() bool {
	return h.source != nil || h.pageJob != nil || h.readyPage != nil || (h.pendingOlderRows != nil && h.pendingOlderRows.HistoryRowCount() > 0)
}

func (h *ChatHistory) prefetchMarginLocked() int {
	pageSize := h.getPageSize()
	return max(h.buffer.Height*6, pageSize/2)
}

// One page is prepared ahead of navigation. The worker owns the source until
// completion; reset/close cancels it without waiting on disk or formatting.
func (h *ChatHistory) prefetchOlderLocked() {
	if h.closed.Load() || (!h.hasOlderLocked()) || h.pageJob != nil || h.readyPage != nil {
		return
	}
	cfg, err := config.Clone(h.a.Config)
	if err != nil {
		h.historyErr = err
		return
	}
	job := &historyPageJob{stop: make(chan struct{}), file: h.turnFile}
	h.pageJob = job
	source := h.source
	page := h.buffer.NewHistoryPage()
	pending := h.pendingOlderRows
	h.pendingOlderRows = nil
	theme, appearance := GetConfiguredTheme(cfg), h.appearanceKey()
	go func() {
		prepared := h.prepareOlderPage(job, source, page, pending, cfg, theme, appearance)
		TerminalMu.Lock()
		defer TerminalMu.Unlock()
		h.finishPreparedPageLocked(job, source, prepared)
	}()
}

func (h *ChatHistory) prepareOlderPage(job *historyPageJob, source displayHistorySource, page *render.Scrollback, pending *render.Scrollback, cfg *config.Config, theme style.UITheme, appearance string) *preparedHistoryPage {
	p := &preparedHistoryPage{rows: page, more: true, appearance: appearance}
	hooks := render.HistoryRenderHooks{}
	if !cfg.MarkdownRender {
		hooks.RenderMarkdown = render.RenderRawMarkdownContent
	}
	targetPageSize := render.HistoryPageSize
	if cfg != nil && cfg.HistoryPageSize > 0 {
		targetPageSize = cfg.HistoryPageSize
	}
	for page.HistoryRowCount() < targetPageSize {
		select {
		case <-job.stop:
			p.err = io.ErrClosedPipe
		default:
		}
		if p.err != nil {
			break
		}

		if pending != nil && pending.HistoryRowCount() > 0 {
			needed := targetPageSize - page.HistoryRowCount()
			avail := pending.HistoryRowCount()
			if avail <= needed {
				count := page.InsertPrepared(0, pending)
				_ = pending.Close()
				pending = nil
				for _, turn := range p.turns {
					turn.start += count
					turn.end += count
				}
				turn := &displayTurn{start: 0, end: count - 1, appearance: appearance}
				p.turns = append([]*displayTurn{turn}, p.turns...)
			} else {
				tail := pending.PopTailRows(needed)
				count := page.InsertPrepared(0, tail)
				_ = tail.Close()
				for _, turn := range p.turns {
					turn.start += count
					turn.end += count
				}
				turn := &displayTurn{start: 0, end: count - 1, appearance: appearance}
				p.turns = append([]*displayTurn{turn}, p.turns...)
				break
			}
			continue
		}

		if source == nil {
			p.more = false
			break
		}
		messages, err := source.Previous()
		if errors.Is(err, io.EOF) {
			p.more = false
			break
		}
		if err != nil {
			p.err = err
			break
		}
		select {
		case <-job.stop:
			p.err = io.ErrClosedPipe
		default:
		}
		if p.err != nil {
			break
		}
		chunk := page.NewHistoryPage()
		render.PrintSessionHistory(historyPageWriter{chunk, job.stop}, messages, theme, cfg, false, cfg.CollapseResults, hooks)
		select {
		case <-job.stop:
			p.err = io.ErrClosedPipe
		default:
		}
		if p.err != nil {
			_ = chunk.Close()
			break
		}
		chunk.FinishHistoryPage()
		if err := chunk.Err(); err != nil {
			p.err = err
			_ = chunk.Close()
			break
		}
		if chunk.EmptyHistoryPage() {
			_ = chunk.Close()
			continue
		}

		needed := targetPageSize - page.HistoryRowCount()
		chunkRows := chunk.HistoryRowCount()
		if chunkRows <= needed {
			count := page.InsertPrepared(0, chunk)
			_ = chunk.Close()
			for _, turn := range p.turns {
				turn.start += count
				turn.end += count
			}
			turn := &displayTurn{start: 0, end: count - 1, messages: messages, appearance: appearance}
			if err := h.archiveTurnTo(turn, job.file); err != nil {
				p.err = err
			}
			p.turns = append([]*displayTurn{turn}, p.turns...)
		} else {
			tail := chunk.PopTailRows(needed)
			count := page.InsertPrepared(0, tail)
			_ = tail.Close()
			for _, turn := range p.turns {
				turn.start += count
				turn.end += count
			}
			turn := &displayTurn{start: 0, end: count - 1, messages: messages, appearance: appearance}
			if err := h.archiveTurnTo(turn, job.file); err != nil {
				p.err = err
			}
			p.turns = append([]*displayTurn{turn}, p.turns...)
			pending = chunk
			break
		}

		if remaining, ok := source.(interface{ HasMore() bool }); ok && !remaining.HasMore() {
			p.more = false
			break
		}
		if p.err != nil {
			break
		}
	}
	p.pending = pending
	select {
	case <-job.stop:
		p.err = io.ErrClosedPipe
	default:
	}
	if p.err != nil {
		p.more = false
		if pending != nil {
			_ = pending.Close()
			p.pending = nil
		}
	}
	if !p.more && (p.pending == nil || p.pending.HistoryRowCount() == 0) {
		if source != nil {
			if err := source.Close(); p.err == nil {
				p.err = err
			}
		}
	} else if p.pending != nil && p.pending.HistoryRowCount() > 0 {
		p.more = true
	}
	return p
}

func (h *ChatHistory) applyOlderPageLocked() int {
	p := h.readyPage
	if p == nil {
		return 0
	}
	if p.err == nil && p.appearance != h.appearanceKey() {
		h.refreshStalePageLocked()
		return 0
	}
	h.readyPage = nil
	defer p.rows.Close()
	if p.rows.Width != h.buffer.Width {
		var anchors []*int
		for _, turn := range p.turns {
			anchors = append(anchors, &turn.start, &turn.end)
		}
		p.rows.Resize(h.buffer.Width, 1, 1, 1, 0, anchors...)
	}
	at := h.historyStart
	added := h.buffer.InsertPrepared(at, p.rows)
	for _, turn := range h.turns {
		turn.start += added
		turn.end += added
	}
	for _, turn := range p.turns {
		turn.start += at
		turn.end += at
	}
	h.turns = append(p.turns, h.turns...)
	h.top += added
	if h.pendingHome {
		h.top = 0
		if h.hasOlderLocked() {
			h.top = h.historyStart
		}
		h.pendingHome = false
	} else if h.pendingUp > 0 {
		minimum := 0
		if h.hasOlderLocked() {
			minimum = h.historyStart
		}
		requested := h.top - h.pendingUp
		h.top = max(minimum, requested)
		h.pendingUp = 0
		if h.hasOlderLocked() {
			h.pendingUp = max(0, minimum-requested)
		}
	}
	// Keep the next page ready, but never drain the full session in advance.
	h.prefetchOlderLocked()
	return added
}
