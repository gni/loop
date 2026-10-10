package db

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// Domain model type aliases preserving complete backward compatibility.
type ToolFunction = message.ToolFunction
type ToolCall = message.ToolCall
type Message = message.Message
type SessionInfo = session.SessionInfo

// ValidateSessionID checks if the provided session identifier contains only allowed characters.
func validateSessionID(sessionID string) error {
	return session.ValidateID(sessionID)
}

// NewUUID generates a cryptographically random RFC 4122 v4 UUID string.
func NewUUID() string {
	return session.NewUUID()
}

// JSONLStore implements session.Store using JSONL files stored in a designated directory.
type JSONLStore struct {
	dir string
	mu  sync.RWMutex
}

var _ session.Store = (*JSONLStore)(nil)

// NewJSONLStore creates an initialized JSONL-backed session storage engine.
func NewJSONLStore(dirPath string) (*JSONLStore, error) {
	clean := filepath.Clean(dirPath)
	if err := os.MkdirAll(clean, 0755); err != nil {
		return nil, fmt.Errorf("failed to create sessions directory: %w", err)
	}
	return &JSONLStore{dir: clean}, nil
}

// SaveMessage appends a message to the specified session.
func (s *JSONLStore) SaveMessage(sessionID string, msg message.Message) error {
	if err := session.ValidateID(sessionID); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	if msg.Role == message.RoleUser && msg.Content == "" {
		return nil
	}

	record := session.Record{
		Timestamp: time.Now().Format("2006-01-02 15:04:05"),
		Message:   msg,
	}

	jsonData, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(jsonData, '\n')); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	_ = s.setLatestSessionIDUnlocked(sessionID)
	return nil
}

// LoadMessages loads all messages for a session.
func (s *JSONLStore) LoadMessages(sessionID string) ([]message.Message, error) {
	if err := session.ValidateID(sessionID); err != nil {
		return nil, err
	}
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	s.mu.RLock()
	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	s.mu.RUnlock()

	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	var messages []message.Message
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var record session.Record
		if err := json.Unmarshal(line, &record); err != nil {
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

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading session file: %w", err)
	}

	return messages, nil
}

// HasMessages checks if a session has any persisted messages.
func (s *JSONLStore) HasMessages(sessionID string) bool {
	if err := session.ValidateID(sessionID); err != nil {
		return false
	}
	if s == nil || s.dir == "" {
		return false
	}

	s.mu.RLock()
	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	s.mu.RUnlock()

	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

// RewriteSession atomically replaces the messages in a session.
func (s *JSONLStore) RewriteSession(sessionID string, messages []message.Message) error {
	if err := session.ValidateID(sessionID); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
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

	now := time.Now().Format("2006-01-02 15:04:05")
	for _, msg := range messages {
		if msg.Role == message.RoleUser && msg.Content == "" {
			continue
		}
		record := session.Record{
			Timestamp: now,
			Message:   msg,
		}
		jsonData, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}
		if _, err := tmpFile.Write(append(jsonData, '\n')); err != nil {
			return fmt.Errorf("failed to write message to temp file: %w", err)
		}
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	targetPath := filepath.Join(s.dir, sessionID+".jsonl")
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("failed to atomically replace session file: %w", err)
	}

	return nil
}

// -------------------------------------------------------------------------
// Global state and backward-compatibility wrappers.
// -------------------------------------------------------------------------

var (
	defaultStore *JSONLStore
	storeMu      sync.RWMutex
)

// InitDB initializes the global session storage directory.
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

func SaveMessage(sessionID string, msg Message) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.SaveMessage(sessionID, msg)
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
	if err != nil {
		return false
	}
	return store.HasMessages(sessionID)
}

func RewriteSession(sessionID string, messages []Message) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.RewriteSession(sessionID, messages)
}
