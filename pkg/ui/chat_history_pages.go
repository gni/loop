package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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
	for start > 0 && s.messages[start].Role != "user" {
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
		OpenHistory(string) (*db.HistoryReader, error)
	}); ok && store != nil {
		r, err = store.OpenHistory(id)
	} else {
		r, err = db.OpenHistory(id)
	}
	if err != nil || r == nil {
		return fallback, err
	}
	if !r.HasMore() {
		_ = r.Close()
		return fallback, nil
	}
	return r, nil
}

// Finished turns keep only disk offsets. Only an active turn or the turn being
// repainted needs a semantic copy in addition to the agent's own context.
func (h *ChatHistory) archiveTurn(turn *displayTurn) error {
	return h.archiveTurnTo(turn, h.turnFile)
}

func (h *ChatHistory) archiveTurnTo(turn *displayTurn, file *os.File) error {
	if len(turn.messages) == 0 {
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

// Read whole turns for correct tool/result pairing, but page their rendered
// rows even when a turn exceeds the display page size.
func (h *ChatHistory) loadOlderLocked() int {
	pageSize := h.getPageSize()
	added := 0
	for (h.source != nil || (h.pendingOlderRows != nil && h.pendingOlderRows.HistoryRowCount() > 0)) && added < pageSize {
		needed := pageSize - added

		if h.pendingOlderRows != nil && h.pendingOlderRows.HistoryRowCount() > 0 {
			avail := h.pendingOlderRows.HistoryRowCount()
			at := h.historyStart
			if avail <= needed {
				delta := h.buffer.InsertPrepared(at, h.pendingOlderRows)
				_ = h.pendingOlderRows.Close()
				h.pendingOlderRows = nil
				for _, turn := range h.turns {
					turn.start += delta
					turn.end += delta
				}
				turn := &displayTurn{start: at, end: at + delta - 1, appearance: h.appearanceKey()}
				h.turns = append([]*displayTurn{turn}, h.turns...)
				h.top += delta
				added += delta
			} else {
				tail := h.pendingOlderRows.PopTailRows(needed)
				delta := h.buffer.InsertPrepared(at, tail)
				_ = tail.Close()
				for _, turn := range h.turns {
					turn.start += delta
					turn.end += delta
				}
				turn := &displayTurn{start: at, end: at + delta - 1, appearance: h.appearanceKey()}
				h.turns = append([]*displayTurn{turn}, h.turns...)
				h.top += delta
				added += delta
				break
			}
			continue
		}

		if h.source == nil {
			break
		}
		messages, err := h.source.Previous()
		if errors.Is(err, io.EOF) {
			if err := h.source.Close(); err != nil {
				h.historyErr = err
			}
			h.source = nil
			break
		}
		if err != nil {
			h.historyErr = err
			_ = h.source.Close()
			h.source = nil
			break
		}
		if remaining, ok := h.source.(interface{ HasMore() bool }); ok && !remaining.HasMore() {
			if err := h.source.Close(); err != nil {
				h.historyErr = err
			}
			h.source = nil
		}

		chunk := h.buffer.NewHistoryPage()
		render.PrintSessionHistory(chunk, messages, GetConfiguredTheme(h.a.Config), h.a.Config, false, h.a.Config.CollapseResults, render.HistoryRenderHooks{})
		chunk.FinishHistoryPage()
		if err := chunk.Err(); err != nil {
			h.historyErr = err
			_ = chunk.Close()
			break
		}
		if chunk.EmptyHistoryPage() {
			_ = chunk.Close()
			continue
		}

		chunkRows := chunk.HistoryRowCount()
		at := h.historyStart
		if chunkRows <= needed {
			delta := h.buffer.InsertPrepared(at, chunk)
			_ = chunk.Close()
			for _, turn := range h.turns {
				turn.start += delta
				turn.end += delta
			}
			turn := &displayTurn{start: at, end: at + delta - 1, messages: messages, appearance: h.appearanceKey()}
			if err := h.archiveTurn(turn); err != nil {
				h.historyErr = err
			}
			h.turns = append([]*displayTurn{turn}, h.turns...)
			h.top += delta
			added += delta
		} else {
			tail := chunk.PopTailRows(needed)
			delta := h.buffer.InsertPrepared(at, tail)
			_ = tail.Close()
			for _, turn := range h.turns {
				turn.start += delta
				turn.end += delta
			}
			turn := &displayTurn{start: at, end: at + delta - 1, messages: messages, appearance: h.appearanceKey()}
			if err := h.archiveTurn(turn); err != nil {
				h.historyErr = err
			}
			h.turns = append([]*displayTurn{turn}, h.turns...)
			h.top += delta
			added += delta
			h.pendingOlderRows = chunk
			break
		}
	}
	return added
}

func (h *ChatHistory) closePagesLocked() {
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
