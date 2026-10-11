package db

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// ClearSession removes a single session from storage.
func (s *JSONLStore) ClearSession(sessionID string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	latestPath := filepath.Join(s.dir, ".latest_session")
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == sessionID {
		_ = os.Remove(latestPath)
	}
	return nil
}

// ClearHistory removes all session files from storage.
func (s *JSONLStore) ClearHistory() error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read sessions directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			name := strings.TrimSuffix(entry.Name(), ".jsonl")
			if err := session.ValidateID(name); err != nil {
				continue
			}
			filePath := filepath.Join(s.dir, entry.Name())
			if err := os.Remove(filePath); err != nil {
				return fmt.Errorf("failed to remove session file %s: %w", entry.Name(), err)
			}
		}
	}

	latestPath := filepath.Join(s.dir, ".latest_session")
	_ = os.Remove(latestPath)

	return nil
}

// SetLatestSessionID writes the active session ID to the latest session pointer file.
func (s *JSONLStore) SetLatestSessionID(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLatestSessionIDUnlocked(sessionID)
}

func (s *JSONLStore) setLatestSessionIDUnlocked(sessionID string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if err := session.ValidateID(sessionID); err != nil {
		return err
	}

	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	now := time.Now()
	_ = os.Chtimes(filePath, now, now)

	latestPath := filepath.Join(s.dir, ".latest_session")
	return os.WriteFile(latestPath, []byte(sessionID), 0644)
}

// GetLatestSessionID retrieves the ID of the most recent session.
func (s *JSONLStore) GetLatestSessionID() (string, error) {
	if s == nil || s.dir == "" {
		return "", fmt.Errorf("sessions directory not initialized")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	latestPath := filepath.Join(s.dir, ".latest_session")
	if data, err := os.ReadFile(latestPath); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" && session.ValidateID(id) == nil {
			sessionFile := filepath.Join(s.dir, id+".jsonl")
			if fi, err := os.Stat(sessionFile); err == nil && !fi.IsDir() {
				return id, nil
			}
		}
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			name := strings.TrimSuffix(entry.Name(), ".jsonl")
			if err := session.ValidateID(name); err != nil {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestFile = entry.Name()
			}
		}
	}

	if latestFile == "" {
		return "", nil
	}

	return strings.TrimSuffix(latestFile, ".jsonl"), nil
}

// GetSessions lists all sessions with metadata ordered descending by timestamp.
func (s *JSONLStore) GetSessions() ([]session.SessionInfo, error) {
	return s.GetSessionsSorted(false)
}

// GetSessionsSorted lists all sessions ordered by timestamp (ascending if true, descending if false).
func (s *JSONLStore) GetSessionsSorted(ascending bool) ([]session.SessionInfo, error) {
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("sessions directory not initialized")
	}

	s.mu.RLock()
	entries, err := os.ReadDir(s.dir)
	s.mu.RUnlock()

	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []session.SessionInfo

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}

		sessionID := strings.TrimSuffix(entry.Name(), ".jsonl")
		if err := session.ValidateID(sessionID); err != nil {
			continue
		}
		filePath := filepath.Join(s.dir, entry.Name())

		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		var firstTimestamp string
		var msgCount int
		var totalTokens int
		var previewParts []string

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

			if record.Message.Role == message.RoleUser && (record.Message.Content == "" || strings.HasPrefix(record.Message.Content, "[user manually executed slash command:")) {
				continue
			}

			msgCount++
			if firstTimestamp == "" {
				firstTimestamp = record.Timestamp
			}

			totalTokens += record.Message.PromptTokens + record.Message.CompletionTokens

			if record.Message.Role != message.RoleSystem && record.Message.Content != "" {
				previewParts = append(previewParts, record.Message.Content)
			}
		}
		f.Close()

		if msgCount == 0 {
			continue
		}

		if firstTimestamp == "" {
			firstTimestamp = entryModTimeOrNow(entry)
		}

		var preview string
		if len(previewParts) > 0 {
			cleanText := strings.TrimSpace(strings.Join(previewParts, " "))
			cleanText = strings.Join(strings.Fields(cleanText), " ")
			runes := []rune(cleanText)
			if len(runes) > 60 {
				preview = string(runes[:60]) + "..."
			} else {
				preview = cleanText
			}
		} else {
			preview = "(No text content)"
		}

		sessions = append(sessions, session.SessionInfo{
			SessionID:   sessionID,
			Timestamp:   firstTimestamp,
			MsgCount:    msgCount,
			Preview:     preview,
			TotalTokens: totalTokens,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if ascending {
			return sessions[i].Timestamp < sessions[j].Timestamp
		}
		return sessions[i].Timestamp > sessions[j].Timestamp
	})

	return sessions, nil
}

// RenameSession changes a session's ID on disk.
func (s *JSONLStore) RenameSession(oldID, newID string) error {
	if err := session.ValidateID(oldID); err != nil {
		return fmt.Errorf("invalid old session ID: %w", err)
	}
	if err := session.ValidateID(newID); err != nil {
		return fmt.Errorf("invalid new session ID: %w", err)
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	oldPath := filepath.Join(s.dir, oldID+".jsonl")
	newPath := filepath.Join(s.dir, newID+".jsonl")

	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("source session does not exist: %w", err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("target session %s already exists", newID)
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename session file: %w", err)
	}

	latestPath := filepath.Join(s.dir, ".latest_session")
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == oldID {
		_ = os.WriteFile(latestPath, []byte(newID), 0644)
	}

	return nil
}

// readEntries parses a session file into DAG records (header line optional).
func readEntries(filePath string) ([]session.Record, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []session.Record
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
		entries = append(entries, record)
	}
	return entries, scanner.Err()
}

// Branch appends a branch marker that moves the active leaf to entryID without
// rewriting history.
func (s *JSONLStore) Branch(sessionID, entryID string) error {
	if err := session.ValidateID(sessionID); err != nil {
		return err
	}
	if s == nil || s.dir == "" {
		return fmt.Errorf("sessions directory not initialized")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filePath := filepath.Join(s.dir, sessionID+".jsonl")
	entries, err := readEntries(filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return fmt.Errorf("entry %s not found in session %s", entryID, sessionID)
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	marker := session.Record{ID: session.NewUUID(), ParentID: path[len(path)-1].ID, Type: "branch", Timestamp: time.Now().Format("2006-01-02 15:04:05")}
	if marker.ParentID == "" {
		for _, e := range entries {
			if e.Type == "message" {
				marker.ParentID = e.ID
			}
		}
	}
	jsonData, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(jsonData, '\n')); err != nil {
		return fmt.Errorf("failed to write branch marker: %w", err)
	}
	return s.setLatestSessionIDUnlocked(sessionID)
}

// ForkSession creates a new session with a versioned header recording
// parentSession and copies the leaf path up to entryID; the parent file is left untouched.
func (s *JSONLStore) ForkSession(parentSessionID, entryID string) (string, error) {
	if err := session.ValidateID(parentSessionID); err != nil {
		return "", err
	}
	if s == nil || s.dir == "" {
		return "", fmt.Errorf("sessions directory not initialized")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := readEntries(filepath.Join(s.dir, parentSessionID+".jsonl"))
	if err != nil {
		return "", fmt.Errorf("failed to read parent session file: %w", err)
	}
	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return "", fmt.Errorf("entry %s not found in session %s", entryID, parentSessionID)
	}

	newID := session.NewUUID()
	filePath := filepath.Join(s.dir, newID+".jsonl")
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create forked session file: %w", err)
	}
	defer f.Close()

	header := session.SessionHeader{Type: "session", Version: session.CurrentSessionVersion, ID: newID, Timestamp: time.Now().Format("2006-01-02 15:04:05"), ParentSession: parentSessionID}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(append(headerJSON, '\n')); err != nil {
		return "", fmt.Errorf("failed to write session header: %w", err)
	}

	var prev string
	for _, e := range path {
		e.ID = session.NewUUID()
		e.ParentID = prev
		e.Type = "message"
		jsonData, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		if _, err := f.Write(append(jsonData, '\n')); err != nil {
			return "", fmt.Errorf("failed to write forked entry: %w", err)
		}
		prev = e.ID
	}

	return newID, nil
}

// Global session helpers

// Branch moves the active leaf of a session to entryID without rewriting history.
func Branch(sessionID, entryID string) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.Branch(sessionID, entryID)
}

func ForkSession(parentSessionID, entryID string) (string, error) {
	store, err := getStore()
	if err != nil {
		return "", err
	}
	return store.ForkSession(parentSessionID, entryID)
}

func ClearHistory() error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.ClearHistory()
}

func ClearSession(sessionID string) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.ClearSession(sessionID)
}

func SetLatestSessionID(sessionID string) error {
	store, err := getStore()
	if err != nil {
		return err
	}
	return store.SetLatestSessionID(sessionID)
}

func GetLatestSessionID() (string, error) {
	store, err := getStore()
	if err != nil {
		return "", err
	}
	return store.GetLatestSessionID()
}

func GetSessions() ([]SessionInfo, error) {
	store, err := getStore()
	if err != nil {
		return nil, err
	}
	return store.GetSessions()
}

func entryModTimeOrNow(entry os.DirEntry) string {
	if info, err := entry.Info(); err == nil {
		return info.ModTime().Format("2006-01-02 15:04:05")
	}
	return time.Now().Format("2006-01-02 15:04:05")
}

