package db

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
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
	file *os.File
	path []historySpan
}

func (s *JSONLStore) OpenHistory(id string) (*HistoryReader, error) {
	if err := s.checkReady(id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	release, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()
	f, err := os.Open(s.sessionPath(id))
	if err != nil {
		return nil, err
	}
	r := &HistoryReader{file: f}
	if err := r.index(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return r, nil
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
	scanner := newJSONLScanner(r.file)
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
			m.ID, m.Parent, m.Type = fmt.Sprintf("legacy-%d", index), prev, "message"
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
		nodes = append(nodes, node{m, historySpan{start, len(data), m.Message.Role, m.Type == "context"}})
		if m.Type != "branch" {
			prev = m.ID
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("index session history: %w", err)
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
	hasMessages := false
	for i := len(reverse) - 1; i >= 0; i-- {
		n := nodes[reverse[i]]
		switch n.Type {
		case "", "message":
			if n.span.role != "" {
				r.path = append(r.path, n.span)
				hasMessages = true
			}
		case "context":
			if !hasMessages {
				r.path = append(r.path, n.span)
				hasMessages = bool(n.Messages)
			}
		}
	}
	return nil
}

func (r *HistoryReader) Previous() ([]message.Message, error) {
	if len(r.path) == 0 {
		return nil, io.EOF
	}
	end := len(r.path)
	start := end - 1
	for start > 0 && r.path[start].role != "user" && !r.path[start].context {
		start--
	}
	var messages []message.Message
	for _, span := range r.path[start:end] {
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
	r.path = r.path[:start]
	return messages, nil
}

func (r *HistoryReader) Close() error { return r.file.Close() }

func (r *HistoryReader) HasMore() bool { return len(r.path) > 0 }
