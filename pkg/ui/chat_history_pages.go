package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/render"
)

type displayHistorySource interface {
	Previous() ([]db.Message, error)
	Close() error
}

type sliceHistorySource struct{ messages []db.Message }

func (s *sliceHistorySource) Previous() ([]db.Message, error) {
	if len(s.messages) == 0 {
		return nil, io.EOF
	}
	start := len(s.messages) - 1
	if s.messages[start].Role == "tool" {
		for start > 0 && s.messages[start].Role == "tool" {
			start--
		}
	}
	if start > 0 && s.messages[start-1].Role == "user" {
		start--
	}
	batch := s.messages[start:]
	s.messages = s.messages[:start]
	return batch, nil
}
func (s *sliceHistorySource) Close() error  { s.messages = nil; return nil }
func (s *sliceHistorySource) HasMore() bool { return len(s.messages) > 0 }

func (h *ChatHistory) openHistory(id string, initial []db.Message) (displayHistorySource, error) {
	fallback := &sliceHistorySource{messages: initial}
	if h.a.Ephemeral || id == "" {
		return fallback, nil
	}
	var r *db.HistoryReader
	var err error
	if store, ok := h.a.Store.(interface {
		OpenHistoryLazy(string) (*db.HistoryReader, error)
	}); ok && store != nil {
		r, err = store.OpenHistoryLazy(id)
	} else if store, ok := h.a.Store.(interface {
		OpenHistory(string) (*db.HistoryReader, error)
	}); ok && store != nil {
		r, err = store.OpenHistory(id)
	} else {
		store, storeErr := db.DefaultStore()
		if storeErr != nil {
			return fallback, storeErr
		}
		r, err = store.OpenHistoryLazy(id)
	}
	if err != nil || r == nil {
		return fallback, err
	}
	if !r.HasMore() {
		_ = r.Close()
		return fallback, nil
	}
	return &chunkedHistoryReader{r}, nil
}

type chunkedHistoryReader struct{ *db.HistoryReader }

func (r *chunkedHistoryReader) Previous() ([]db.Message, error) {
	return r.PreviousChunk()
}

// Finished turns keep only disk offsets. Only an active turn or the turn being
// repainted needs a semantic copy in addition to the agent's own context.
func (h *ChatHistory) archiveTurn(turn *displayTurn) error {
	return h.archiveTurnTo(turn, h.turnFile)
}

func (h *ChatHistory) archiveTurnTo(turn *displayTurn, file *os.File) error {
	if len(turn.messages) == 0 || file == nil {
		return nil
	}
	data, err := json.Marshal(turn.messages)
	if err != nil {
		return fmt.Errorf("save display turn: %w", err)
	}
	h.turnMu.Lock()
	if file == nil || h.turnFile != file {
		h.turnMu.Unlock()
		return os.ErrClosed
	}
	offset := h.turnOffset
	h.turnOffset += int64(len(data))
	h.turnMu.Unlock()
	// Reserve disjoint ranges and release the lifecycle lock between writes.
	// A large background turn must not hold reset/close through JSON encoding
	// or one very large disk write.
	for start := 0; start < len(data); {
		end := min(len(data), start+64*1024)
		h.turnMu.Lock()
		if h.turnFile != file {
			h.turnMu.Unlock()
			return os.ErrClosed
		}
		n, err := file.WriteAt(data[start:end], offset+int64(start))
		h.turnMu.Unlock()
		if err == nil && n != end-start {
			err = io.ErrShortWrite
		}
		if err != nil {
			return fmt.Errorf("save display turn: %w", err)
		}
		start = end
	}
	turn.offset, turn.size = offset, len(data)
	turn.messages = nil
	return nil
}

func (h *ChatHistory) readTurnMessagesFromFile(file *os.File, offset int64, size int) ([]db.Message, error) {
	if size <= 0 {
		return nil, nil
	}
	data := make([]byte, size)
	for start := 0; start < size; {
		end := min(size, start+64*1024)
		h.turnMu.Lock()
		if file == nil || h.turnFile != file {
			h.turnMu.Unlock()
			return nil, os.ErrClosed
		}
		n, err := file.ReadAt(data[start:end], offset+int64(start))
		h.turnMu.Unlock()
		if err != nil {
			return nil, err
		}
		if n != end-start {
			return nil, io.ErrUnexpectedEOF
		}
		start = end
	}
	var messages []db.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (h *ChatHistory) turnMessages(turn *displayTurn) ([]db.Message, error) {
	if turn.messages != nil || turn.size == 0 {
		return turn.messages, nil
	}
	h.turnMu.Lock()
	file := h.turnFile
	h.turnMu.Unlock()
	return h.readTurnMessagesFromFile(file, turn.offset, turn.size)
}

func (h *ChatHistory) getPageSize() int {
	if h.a != nil && h.a.Config != nil && h.a.Config.HistoryPageSize > 0 {
		return h.a.Config.HistoryPageSize
	}
	if h.buffer != nil {
		return h.buffer.PageSize()
	}
	return render.HistoryPageSize
}

// Synchronous callers share the same page builder as the background loader.
func (h *ChatHistory) loadOlderLocked() int {
	if h.pageJob != nil {
		return 0
	}
	if h.readyPage != nil {
		return h.applyOlderPageLocked()
	}
	cfg, err := config.Clone(h.a.Config)
	if err != nil {
		h.historyErr = err
		return 0
	}
	job := &historyPageJob{stop: make(chan struct{}), file: h.turnFile}
	page := h.buffer.NewHistoryPage()
	p := h.prepareOlderPage(job, h.source, page, h.pendingOlderRows, cfg, GetConfiguredTheme(cfg), h.appearanceKey(), false)
	h.pendingOlderRows = p.pending
	if !p.more {
		h.source = nil
	}
	if p.err != nil {
		h.historyErr = p.err
	}
	if len(p.turns) == 0 {
		_ = p.rows.Close()
		return 0
	}
	h.readyPage = p
	return h.applyOlderPageLocked()
}

func (h *ChatHistory) closePagesLocked() {
	h.initialLoading, h.initialOutput = false, false
	h.initialFilling = false
	h.initialRows = 0
	if h.layoutJob != nil {
		if !h.layoutJob.cancelled {
			close(h.layoutJob.stop)
		}
		h.layoutJob = nil
	}
	h.layoutScroll = 0
	for _, turn := range h.turns {
		if turn.alternate != nil {
			_ = turn.alternate.rows.Close()
			turn.alternate = nil
		}
	}

	if h.pageJob != nil {
		close(h.pageJob.stop)
		h.pageJob = nil
		h.source = nil
	}
	if h.readyPage != nil {
		_ = h.readyPage.rows.Close()
		h.readyPage = nil
	}
	if h.pendingOlderRows != nil {
		_ = h.pendingOlderRows.Close()
		h.pendingOlderRows = nil
	}
	h.pendingUp = 0
	h.pendingHome = false
	h.scrollDirection = 0
	if h.source != nil {
		_ = h.source.Close()
		h.source = nil
	}
	if h.buffer != nil {
		_ = h.buffer.Close()
	}
	h.turnMu.Lock()
	defer h.turnMu.Unlock()
	if h.turnFile != nil {
		name := h.turnFile.Name()
		_ = h.turnFile.Close()
		_ = os.Remove(name)
		h.turnFile = nil
	}
}
