package db

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

type ToolFunction = message.ToolFunction
type ToolCall = message.ToolCall
type Message = message.Message
type SessionInfo = session.SessionInfo

func NewUUID() string {
	return session.NewUUID()
}

type JSONLStore struct {
	dir string
	mu  sync.RWMutex
}

var _ session.Store = (*JSONLStore)(nil)

func NewJSONLStore(dirPath string) (*JSONLStore, error) {
	clean := filepath.Clean(dirPath)
	if err := os.MkdirAll(clean, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}
	return &JSONLStore{dir: clean}, nil
}

func (s *JSONLStore) sessionPath(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".jsonl")
}

func (s *JSONLStore) latestSessionPath() string {
	return filepath.Join(s.dir, ".latest_session")
}

func nowTimestamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

func validSessionEntry(entry os.DirEntry) (string, bool) {
	if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
		return "", false
	}
	sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
	if session.ValidateID(sessionID) != nil {
		return "", false
	}
	return sessionID, true
}

func writeJSONLine(w io.Writer, v any) error {
	jsonData, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(append(jsonData, '\n'))
	return err
}

func (s *JSONLStore) checkReady(sessionID string) error {
	if sessionID != "" {
		if err := session.ValidateID(sessionID); err != nil {
			return err
		}
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}
	return nil
}

func (s *JSONLStore) SaveMessage(sessionID string, msg message.Message) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}
	if msg.Role == message.RoleUser && msg.Content == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := s.sessionPath(sessionID)
	var parentID string
	if entries, err := readEntries(filePath); err == nil && len(entries) > 0 {
		if leaf := session.LeafPath(entries, ""); len(leaf) > 0 {
			parentID = leaf[len(leaf)-1].ID
		}
	}

	record := session.Record{
		ID:        session.NewUUID(),
		ParentID:  parentID,
		Type:      "message",
		Timestamp: nowTimestamp(),
		Message:   msg,
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if err := writeJSONLine(f, record); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	_ = s.setLatestSessionIDUnlocked(sessionID)
	return nil
}

func (s *JSONLStore) LoadMessages(sessionID string) ([]message.Message, error) {
	if err := s.checkReady(sessionID); err != nil {
		return nil, err
	}

	s.mu.RLock()
	filePath := s.sessionPath(sessionID)
	entries, err := readEntries(filePath)
	s.mu.RUnlock()

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}

	activeRecords := session.LeafPath(entries, "")
	if len(activeRecords) == 0 && len(entries) > 0 {
		activeRecords = entries
	}

	var messages []message.Message
	for _, record := range activeRecords {
		if record.Type == "branch" || record.Type == "session" || record.Message.Role == "" {
			continue
		}
		if record.Message.Role == message.RoleUser && record.Message.Content == "" {
			continue
		}
		if strings.HasPrefix(record.Message.Content, "[user manually executed slash command:") {
			continue
		}
		messages = append(messages, record.Message)
	}

	return messages, nil
}

func (s *JSONLStore) HasMessages(sessionID string) bool {
	if s.checkReady(sessionID) != nil {
		return false
	}
	s.mu.RLock()
	filePath := s.sessionPath(sessionID)
	s.mu.RUnlock()

	info, err := os.Stat(filePath)
	return err == nil && info.Size() > 0
}

func (s *JSONLStore) RewriteSession(sessionID string, messages []message.Message) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tmpFile, err := os.CreateTemp(s.dir, sessionID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp session file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	now := nowTimestamp()
	var parentID string
	for _, msg := range messages {
		if msg.Role == message.RoleUser && msg.Content == "" {
			continue
		}
		id := session.NewUUID()
		record := session.Record{
			ID:        id,
			ParentID:  parentID,
			Type:      "message",
			Timestamp: now,
			Message:   msg,
		}
		parentID = id
		if err := writeJSONLine(tmpFile, record); err != nil {
			return fmt.Errorf("failed to write message to temp file: %w", err)
		}
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.sessionPath(sessionID)); err != nil {
		return fmt.Errorf("failed to atomically replace session file: %w", err)
	}

	return nil
}

var (
	defaultStore *JSONLStore
	storeMu      sync.RWMutex
)

func InitDB(dirPath string) error {
	store, err := NewJSONLStore(dirPath)
	if err != nil {
		return err
	}
	storeMu.Lock()
	defaultStore = store
	storeMu.Unlock()
	return nil
}

func getStore() (*JSONLStore, error) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	if defaultStore == nil {
		return nil, fmt.Errorf("sessions directory not initialized")
	}
	return defaultStore, nil
}

func withStore(fn func(*JSONLStore) error) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return fn(store)
}

func SaveMessage(sessionID string, msg Message) error {
	return withStore(func(s *JSONLStore) error { return s.SaveMessage(sessionID, msg) })
}

func LoadMessages(sessionID string) ([]Message, error) {
	store, err := getStore()
	if err != nil {
		return nil, err
	}
	return store.LoadMessages(sessionID)
}

func HasMessages(sessionID string) bool {
	store, err := getStore()
	return err == nil && store.HasMessages(sessionID)
}

func RewriteSession(sessionID string, messages []Message) error {
	return withStore(func(s *JSONLStore) error { return s.RewriteSession(sessionID, messages) })
}