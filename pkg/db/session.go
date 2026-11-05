package db

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

func (s *JSONLStore) ClearSession(sessionID string) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	if err := os.Remove(s.sessionPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	delete(s.core.cache, sessionID)
	delete(s.core.summaryCache, sessionID)
	latestPath := s.latestSessionPath()
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == sessionID {
		if err := os.Remove(latestPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove latest-session pointer: %w", err)
		}
	}
	return nil
}

func (s *JSONLStore) ClearHistory() error {
	if err := s.checkReady(""); err != nil {
		return err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	entries, err := os.ReadDir(s.core.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read sessions directory: %w", err)
	}

	for _, entry := range entries {
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		if err := os.Remove(s.sessionPath(sessionID)); err != nil {
			return fmt.Errorf("failed to remove session file %s: %w", entry.Name(), err)
		}
	}

	if err := os.Remove(s.latestSessionPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove latest-session pointer: %w", err)
	}
	clear(s.core.cache)
	clear(s.core.summaryCache)
	return nil
}

func (s *JSONLStore) SetLatestSessionID(sessionID string) error {
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()
	return s.setLatestSessionIDUnlocked(sessionID)
}

func (s *JSONLStore) setLatestSessionIDUnlocked(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	path := s.sessionPath(sessionID)
	now := time.Now()
	if os.Chtimes(path, now, now) == nil {
		if info, statErr := os.Stat(path); statErr == nil {
			if c, ok := s.core.cache[sessionID]; ok && c.size == info.Size() {
				c.modified = info.ModTime()
				s.core.cache[sessionID] = c
			}
			if sum, ok := s.core.summaryCache[sessionID]; ok && sum.size == info.Size() {
				sum.modified = info.ModTime()
				s.core.summaryCache[sessionID] = sum
			}
		}
	}
	return s.writeLatestID(sessionID)
}

func (s *JSONLStore) writeLatestID(sessionID string) error {
	f, err := os.CreateTemp(s.core.dir, ".latest-session-*")
	if err != nil {
		return fmt.Errorf("create latest-session pointer: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fmt.Errorf("secure latest-session pointer: %w", err)
	}
	if _, err := f.WriteString(sessionID); err != nil {
		f.Close()
		return fmt.Errorf("write latest-session pointer: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync latest-session pointer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close latest-session pointer: %w", err)
	}
	if err := os.Rename(f.Name(), s.latestSessionPath()); err != nil {
		return fmt.Errorf("replace latest-session pointer: %w", err)
	}
	return nil
}

func (s *JSONLStore) GetLatestSessionID() (string, error) {
	if err := s.checkReady(""); err != nil {
		return "", err
	}

	s.core.mu.RLock()
	defer s.core.mu.RUnlock()

	if data, err := os.ReadFile(s.latestSessionPath()); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" && session.ValidateID(id) == nil {
			if fi, err := os.Stat(s.sessionPath(id)); err == nil && !fi.IsDir() {
				return id, nil
			}
		}
	}

	entries, err := os.ReadDir(s.core.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		name, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		if info, err := entry.Info(); err == nil && info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = name
		}
	}

	return latestFile, nil
}

func (s *JSONLStore) GetSessions() ([]session.SessionInfo, error) {
	return s.GetSessionsSorted(false)
}

func appendPreviewSnippet(builder *strings.Builder, runeCount *int, text string) {
	if *runeCount > 64 || text == "" {
		return
	}
	for _, field := range strings.Fields(text) {
		if builder.Len() > 0 {
			builder.WriteByte(' ')
			*runeCount++
		}
		for _, r := range field {
			builder.WriteRune(r)
			*runeCount++
			if *runeCount > 64 {
				return
			}
		}
	}
}

func (s *JSONLStore) summarizeSessionLocked(sessionID string, entry os.DirEntry) (cachedSummary, bool) {
	info, err := entry.Info()
	if err != nil {
		return cachedSummary{}, false
	}
	if sum, ok := s.core.summaryCache[sessionID]; ok && sum.size == info.Size() && sum.modified.Equal(info.ModTime()) {
		return sum, true
	}

	f, err := os.Open(s.sessionPath(sessionID))
	if err != nil {
		return cachedSummary{}, false
	}
	defer f.Close()

	var startTime, firstTimestamp string
	var cwd, displayName, parentSession string
	var msgCount, contextMsgCount, totalTokens int
	var previewBuilder, contextPreviewBuilder strings.Builder
	var previewRunes, contextPreviewRunes int
	var prompts []string
	var lastPrompt string

	scanner := newJSONLScanner(f)
	lineNo := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		lineNo++

		var record session.Record
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		if lineNo == 1 && record.Timestamp != "" {
			startTime = record.Timestamp
		}
		if record.Type == "session" {
			cwd = record.Cwd
			parentSession = record.ParentSession
			continue
		}
		if record.Type == "name" {
			displayName = record.Name
			continue
		}
		if record.Type == "context" || record.Type == "compaction" {
			contextMsgCount = 0
			contextPreviewBuilder.Reset()
			contextPreviewRunes = 0
			for _, m := range record.Messages {
				if isSkippedSlashRecord(m) || m.Role == "" {
					continue
				}
				contextMsgCount++
				if m.Role != message.RoleSystem && m.Content != "" {
					appendPreviewSnippet(&contextPreviewBuilder, &contextPreviewRunes, m.Content)
				}
			}
			if firstTimestamp == "" && record.Timestamp != "" {
				firstTimestamp = record.Timestamp
			}
			continue
		}
		if record.Message.Role == "" || isSkippedSlashRecord(record.Message) {
			continue
		}
		if record.Type == "prompt" {
			continue
		}

		msgCount++
		if firstTimestamp == "" {
			firstTimestamp = record.Timestamp
		}
		totalTokens += record.Message.PromptTokens + record.Message.CompletionTokens

		if record.Message.Role != message.RoleSystem && record.Message.Content != "" {
			appendPreviewSnippet(&previewBuilder, &previewRunes, record.Message.Content)
		}
		if record.Message.Role == message.RoleUser && !strings.HasPrefix(strings.ToLower(record.Message.Content), "[user manually executed") {
			trimmed := strings.TrimSpace(record.Message.Content)
			if trimmed != "" && trimmed != lastPrompt {
				prompts = append(prompts, trimmed)
				lastPrompt = trimmed
			}
		}
	}

	effectiveCount := msgCount
	rawPreview := previewBuilder.String()
	if effectiveCount == 0 && contextMsgCount > 0 {
		effectiveCount = contextMsgCount
		rawPreview = contextPreviewBuilder.String()
	}

	if startTime == "" {
		startTime = info.ModTime().Format("2006-01-02 15:04:05")
	}
	if firstTimestamp == "" {
		firstTimestamp = startTime
	}

	preview := "(No text content)"
	if rawPreview != "" {
		if runes := []rune(rawPreview); len(runes) > 60 {
			preview = string(runes[:60]) + "..."
		} else {
			preview = rawPreview
		}
	}

	sum := cachedSummary{
		size:       info.Size(),
		modified:   info.ModTime(),
		hasVisible: effectiveCount > 0,
		startTime:  startTime,
		prompts:    prompts,
		info: session.SessionInfo{
			SessionID:     sessionID,
			Timestamp:     firstTimestamp,
			MsgCount:      effectiveCount,
			Preview:       preview,
			TotalTokens:   totalTokens,
			Name:          displayName,
			Cwd:           cwd,
			ParentSession: parentSession,
		},
	}
	s.core.summaryCache[sessionID] = sum
	return sum, true
}

func (s *JSONLStore) GetSessionsSorted(ascending bool) ([]session.SessionInfo, error) {
	if err := s.checkReady(""); err != nil {
		return nil, err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := os.ReadDir(s.core.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []session.SessionInfo
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		seen[sessionID] = true
		sum, ok := s.summarizeSessionLocked(sessionID, entry)
		if !ok || !sum.hasVisible {
			continue
		}
		sessions = append(sessions, sum.info)
	}
	for cachedID := range s.core.summaryCache {
		if !seen[cachedID] {
			delete(s.core.summaryCache, cachedID)
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		if ascending {
			return sessions[i].Timestamp < sessions[j].Timestamp
		}
		return sessions[i].Timestamp > sessions[j].Timestamp
	})

	return sessions, nil
}

func (s *JSONLStore) RenameSession(oldID, newID string) error {
	if err := session.ValidateID(oldID); err != nil {
		return fmt.Errorf("invalid old session ID: %w", err)
	}
	if err := s.checkReady(newID); err != nil {
		return fmt.Errorf("invalid new session ID: %w", err)
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	oldPath := s.sessionPath(oldID)
	newPath := s.sessionPath(newID)

	if _, err := os.Stat(oldPath); err != nil {
		return fmt.Errorf("source session does not exist: %w", err)
	}
	if _, err := os.Stat(newPath); err == nil {
		return fmt.Errorf("target session %s already exists", newID)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("failed to rename session file: %w", err)
	}

	delete(s.core.cache, oldID)
	delete(s.core.cache, newID)
	delete(s.core.summaryCache, oldID)
	delete(s.core.summaryCache, newID)
	latestPath := s.latestSessionPath()
	if data, err := os.ReadFile(latestPath); err == nil && strings.TrimSpace(string(data)) == oldID {
		if err := s.writeLatestID(newID); err != nil {
			return fmt.Errorf("session renamed, but latest pointer update failed: %w", err)
		}
	}
	return nil
}

func readEntries(filePath string) ([]session.Record, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []session.Record
	scanner := newJSONLScanner(f)
	line := 0
	for scanner.Scan() {
		line++
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var entry session.Record
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf("session record %d is corrupt: %w", line, err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	if err := session.ValidateEntries(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *JSONLStore) Branch(sessionID, entryID string) error {
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return err
	}
	defer release()

	filePath := s.sessionPath(sessionID)
	entries, err := readEntries(filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return fmt.Errorf("entry %s not found in session %s", entryID, sessionID)
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()

	marker := session.Record{
		ID:        session.NewUUID(),
		ParentID:  path[len(path)-1].ID,
		Type:      "branch",
		Timestamp: nowTimestamp(),
	}
	if marker.ParentID == "" {
		for _, e := range entries {
			if e.Type == "message" {
				marker.ParentID = e.ID
			}
		}
	}
	if err := writeJSONLine(f, marker); err != nil {
		return fmt.Errorf("failed to write branch marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	delete(s.core.cache, sessionID)
	delete(s.core.summaryCache, sessionID)
	return s.setLatestSessionIDUnlocked(sessionID)
}

func (s *JSONLStore) ForkSession(parentSessionID, entryID string) (string, error) {
	if err := s.checkReady(parentSessionID); err != nil {
		return "", err
	}

	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	release, err := s.lock(true)
	if err != nil {
		return "", err
	}
	defer release()

	entries, err := readEntries(s.sessionPath(parentSessionID))
	if err != nil {
		return "", fmt.Errorf("failed to read parent session file: %w", err)
	}
	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return "", fmt.Errorf("entry %s not found in session %s", entryID, parentSessionID)
	}

	newID := session.NewUUID()
	f, err := os.CreateTemp(s.core.dir, ".fork-*")
	if err != nil {
		return "", fmt.Errorf("failed to create forked session file: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return "", fmt.Errorf("failed to secure forked session file: %w", err)
	}

	header := session.SessionHeader{
		Type:          "session",
		Version:       session.CurrentSessionVersion,
		ID:            newID,
		Timestamp:     nowTimestamp(),
		ParentSession: parentSessionID,
		Cwd:           s.Workspace,
	}
	if err := writeJSONLine(f, header); err != nil {
		f.Close()
		return "", fmt.Errorf("failed to write session header: %w", err)
	}

	var prev string
	for _, e := range path {
		if e.ID == "" || strings.HasPrefix(e.ID, "legacy-") {
			e.ID = session.NewUUID()
		}
		e.ParentID = prev
		if err := writeJSONLine(f, e); err != nil {
			f.Close()
			return "", fmt.Errorf("failed to write forked entry: %w", err)
		}
		prev = e.ID
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), s.sessionPath(newID)); err != nil {
		return "", fmt.Errorf("failed to finalize forked session file: %w", err)
	}
	return newID, nil
}

func ForkSession(parentSessionID, entryID string) (string, error) {
	return withStoreVal(func(s *JSONLStore) (string, error) { return s.ForkSession(parentSessionID, entryID) })
}

func ClearHistory() error {
	return withStore(func(s *JSONLStore) error { return s.ClearHistory() })
}

func ClearSession(sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.ClearSession(sessionID) })
}

func SetLatestSessionID(sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.SetLatestSessionID(sessionID) })
}

func GetSessions() ([]SessionInfo, error) {
	return withStoreVal(func(s *JSONLStore) ([]SessionInfo, error) { return s.GetSessions() })
}

func Branch(sessionID, entryID string) error {
	return withStore(func(s *JSONLStore) error { return s.Branch(sessionID, entryID) })
}

func Entries(sessionID string) ([]session.Record, error) {
	return withStoreVal(func(s *JSONLStore) ([]session.Record, error) { return s.Entries(sessionID) })
}

func RenameSession(oldID, newID string) error {
	return withStore(func(s *JSONLStore) error { return s.RenameSession(oldID, newID) })
}

func RewriteSession(sessionID string, messages []message.Message) error {
	return withStore(func(s *JSONLStore) error { return s.RewriteSession(sessionID, messages) })
}
