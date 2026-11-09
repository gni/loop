package db

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

type historySpan struct {
	offset  int64
	size    int
	role    string
	context bool
}

type historyMessagesPresent bool

func (p *historyMessagesPresent) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if !bytes.Equal(data, []byte("null")) && (len(data) < 2 || data[0] != '[' || data[len(data)-1] != ']') {
		return fmt.Errorf("session messages must be an array")
	}
	*p = len(data) > 2 && data[0] == '[' && len(bytes.TrimSpace(data[1:len(data)-1])) > 0
	return nil
}

// HistoryReader snapshots the active branch's record offsets, not its message
// bodies. Previous reads one conversational turn from disk, newest turn first.
// It is independent of the store's model-context projection and cache.
type HistoryReader struct {
	cancelled atomic.Bool
	mu        sync.Mutex
	store     *JSONLStore
	file      *os.File
	path      []historySpan
	indexed   bool
	size      int64
	ctx       context.Context
	cancel    context.CancelFunc
}

// Cancel interrupts indexing and page reads without waiting for the reader lock.
func (r *HistoryReader) Cancel() {
	r.cancelled.Store(true)
	if r.cancel != nil {
		r.cancel()
	}
}

type cancellableHistoryInput struct {
	io.Reader
	cancelled *atomic.Bool
}

func (r cancellableHistoryInput) Read(data []byte) (int, error) {
	if r.cancelled.Load() {
		return 0, context.Canceled
	}
	return r.Reader.Read(data)
}

func (s *JSONLStore) OpenHistory(id string) (*HistoryReader, error) {
	return s.OpenHistoryContext(context.Background(), id)
}

func (s *JSONLStore) OpenHistoryContext(ctx context.Context, id string) (*HistoryReader, error) {
	return s.openHistory(ctx, id, false)
}

// OpenHistoryLazy snapshots the file immediately; its index is built by the
// background page reader on the first Previous call.
func (s *JSONLStore) OpenHistoryLazy(id string) (*HistoryReader, error) {
	return s.OpenHistoryLazyContext(context.Background(), id)
}

func (s *JSONLStore) OpenHistoryLazyContext(ctx context.Context, id string) (*HistoryReader, error) {
	return s.openHistory(ctx, id, true)
}

func (s *JSONLStore) openHistory(ctx context.Context, id string, lazy bool) (*HistoryReader, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkReady(id); err != nil {
		return nil, err
	}
	release, err := s.fileLock(ctx, false)
	if err != nil {
		return nil, err
	}
	defer release()
	f, err := storage.OpenRegularFile(s.sessionPath(id))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	readerCtx, cancel := context.WithCancel(ctx)
	r := &HistoryReader{store: s, file: f, size: info.Size(), ctx: readerCtx, cancel: cancel}
	if !lazy {
		if err := r.index(); err != nil {
			cancel()
			_ = f.Close()
			return nil, err
		}
		r.indexed = true
	}
	return r, nil
}

func (r *HistoryReader) context() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

func (r *HistoryReader) index() error {
	// Bodies are deliberately absent from this shape. JSON decoding validates
	// them without retaining their potentially very large content strings.
	type meta struct {
		ID       string                 `json:"id"`
		Parent   string                 `json:"parentId"`
		Type     string                 `json:"type"`
		Version  int                    `json:"version"`
		Messages historyMessagesPresent `json:"messages"`
		Message  struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	type node struct {
		meta
		span historySpan
	}
	var nodes []node
	ids := make(map[string]int)
	var prev string
	var offset int64
	scanner := newJSONLScanner(contextReader{r.context(), cancellableHistoryInput{io.NewSectionReader(r.file, 0, r.size), &r.cancelled}})
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	line, record := 0, 0
	for scanner.Scan() {
		if r.cancelled.Load() {
			return context.Canceled
		}
		if err := r.context().Err(); err != nil {
			return err
		}
		data := scanner.Bytes()
		start := offset
		offset += int64(len(data)) + 1
		line++
		if len(bytes.TrimSuffix(data, []byte{'\r'})) == 0 {
			continue
		}
		var m meta
		if err := json.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("session record %d is corrupt: %w", line, err)
		}
		index := record
		record++
		if m.Type == "session" {
			if index != 0 || m.Version > session.CurrentSessionVersion {
				return fmt.Errorf("unsupported session header at entry %d", index)
			}
			continue
		}
		if m.ID == "" {
			m.ID, m.Parent = fmt.Sprintf("legacy-%d", index), prev
			if m.Type == "" {
				m.Type = "message"
			}
		} else {
			if _, ok := ids[m.ID]; ok {
				return fmt.Errorf("duplicate session entry %q", m.ID)
			}
			if m.Parent != "" {
				if _, ok := ids[m.Parent]; !ok {
					return fmt.Errorf("entry %q has unknown parent %q", m.ID, m.Parent)
				}
			}
		}
		ids[m.ID] = len(nodes)
		nodes = append(nodes, node{m, historySpan{start, len(data), m.Message.Role, m.Type == "context" || m.Type == "compaction"}})
		if m.Type != "branch" {
			prev = m.ID
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("index session history: %w", err)
	}
	if err := r.context().Err(); err != nil {
		return err
	}
	// Match LeafPath, including branch markers and legacy parent IDs.
	var reverse []int
	for i := len(nodes) - 1; i >= 0; {
		reverse = append(reverse, i)
		parent := nodes[i].Parent
		if parent == "" {
			break
		}
		var ok bool
		i, ok = ids[parent]
		if !ok {
			return fmt.Errorf("session has an invalid entry path")
		}
		if len(reverse) > len(nodes) {
			return fmt.Errorf("session has a cyclic entry path")
		}
	}
	var spans []historySpan
	hasMessages := false
	for i := len(reverse) - 1; i >= 0; i-- {
		n := nodes[reverse[i]]
		switch n.Type {
		case "", "message":
			if n.span.role != "" {
				spans = append(spans, n.span)
				hasMessages = true
			}
		case "context", "compaction":
			if !hasMessages {
				spans = append(spans, n.span)
				hasMessages = bool(n.Messages)
			}
		}
	}
	if err := r.context().Err(); err != nil {
		return err
	}
	r.path = spans
	return nil
}

func (r *HistoryReader) Previous() ([]message.Message, error) {
	return r.previous(false)
}

// PreviousChunk reads the latest message block, keeping calls and their tool
// results together, without decoding an entire long conversational turn.
func (r *HistoryReader) PreviousChunk() ([]message.Message, error) {
	return r.previous(true)
}

func (r *HistoryReader) previous(chunk bool) ([]message.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelled.Load() {
		return nil, context.Canceled
	}
	if err := r.context().Err(); err != nil {
		return nil, err
	}
	if r.file == nil {
		return nil, io.EOF
	}
	if r.store != nil {
		release, err := r.store.fileLock(r.context(), false)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	if !r.indexed {
		if err := r.index(); err != nil {
			return nil, err
		}
		r.indexed = true
	}
	if len(r.path) == 0 {
		return nil, io.EOF
	}
	end := len(r.path)
	start := end - 1
	if chunk {
		if r.path[start].role == "tool" {
			for start > 0 && r.path[start].role == "tool" && !r.path[start].context {
				start--
			}
		}
		if start > 0 && r.path[start-1].role == "user" {
			start--
		}
	} else {
		for start > 0 && r.path[start].role != "user" && !r.path[start].context {
			start--
		}
	}
	var messages []message.Message
	for _, span := range r.path[start:end] {
		if r.cancelled.Load() {
			return nil, context.Canceled
		}
		if err := r.context().Err(); err != nil {
			return nil, err
		}
		data := make([]byte, span.size)
		if _, err := r.file.ReadAt(data, span.offset); err != nil {
			return nil, fmt.Errorf("read session history: %w", err)
		}
		var entry session.Record
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, fmt.Errorf("decode session history: %w", err)
		}
		if span.context {
			messages = append(messages, entry.Messages...)
		} else {
			messages = append(messages, entry.Message)
		}
	}
	if err := r.context().Err(); err != nil {
		return nil, err
	}
	r.path = r.path[:start]
	return messages, nil
}

func (r *HistoryReader) Close() error {
	r.Cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	r.path = nil
	return err
}

func (r *HistoryReader) HasMore() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file != nil && ((!r.indexed && r.size > 0) || len(r.path) > 0)
}
