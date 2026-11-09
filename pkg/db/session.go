package db

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

func (s *JSONLStore) ClearSession(sessionID string) error {
	return s.ClearSessionContext(context.Background(), sessionID)
}

// ClearSessionContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ClearSessionContext(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()

	if err := os.Remove(s.sessionPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete session file: %w", err)
	}
	s.invalidateSessionCacheLocked(sessionID)
	latestPath := s.latestSessionPath()
	if data, err := storage.ReadFile(latestPath, 4096); err == nil && strings.TrimSpace(string(data)) == sessionID {
		if err := os.Remove(latestPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove latest-session pointer: %w", err)
		}
	}
	return nil
}

func (s *JSONLStore) ClearHistory() error {
	return s.ClearHistoryContext(context.Background())
}

// ClearHistoryContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ClearHistoryContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(""); err != nil {
		return err
	}

	release, err := s.lockStore(ctx, true)
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
		if err := os.Remove(s.sessionPath(sessionID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove session file %s: %w", entry.Name(), err)
		}
		s.invalidateSessionCacheLocked(sessionID)
	}

	if err := os.Remove(s.latestSessionPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove latest-session pointer: %w", err)
	}
	clear(s.core.cache)
	clear(s.core.summaryCache)
	return nil
}

func (s *JSONLStore) SetLatestSessionID(sessionID string) error {
	return s.SetLatestSessionIDContext(context.Background(), sessionID)
}

// SetLatestSessionIDContext allows cancellation while waiting for session storage.
func (s *JSONLStore) SetLatestSessionIDContext(ctx context.Context, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if err := s.checkReady(sessionID); err != nil {
		return err
	}
	release, err := s.lockStore(ctx, true)
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
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
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
	return s.GetLatestSessionIDContext(context.Background())
}

// GetLatestSessionIDContext allows cancellation while waiting for session storage.
func (s *JSONLStore) GetLatestSessionIDContext(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkReady(""); err != nil {
		return "", err
	}

	release, err := s.lockStore(ctx, false)
	if err != nil {
		return "", err
	}
	defer release()

	if data, err := storage.ReadFile(s.latestSessionPath(), 4096); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" && session.ValidateID(id) == nil {
			if fi, err := os.Stat(s.sessionPath(id)); err == nil && fi.Mode().IsRegular() {
				if sum, ok := s.summarizeSessionLockedContext(ctx, id, fs.FileInfoToDirEntry(fi)); ok && sum.hasVisible {
					return id, nil
				}
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		if sum, ok := s.summarizeSessionLockedContext(ctx, name, entry); ok && sum.hasVisible {
			if info, err := entry.Info(); err == nil && info.ModTime().After(latestTime) {
				latestTime = info.ModTime()
				latestFile = name
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return "", err
	}
	return latestFile, nil
}

func (s *JSONLStore) GetSessions() ([]session.SessionInfo, error) {
	return s.GetSessionsContext(context.Background())
}

// GetSessionsContext allows cancellation while waiting for session storage.
func (s *JSONLStore) GetSessionsContext(ctx context.Context) ([]session.SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.GetSessionsSortedContext(ctx, false)
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
	return s.summarizeSessionLockedContext(context.Background(), sessionID, entry)
}

func (s *JSONLStore) summarizeSessionLockedContext(ctx context.Context, sessionID string, entry os.DirEntry) (cachedSummary, bool) {
	if ctx.Err() != nil {
		return cachedSummary{}, false
	}
	info, err := entry.Info()
	if err != nil {
		return cachedSummary{}, false
	}
	if sum, ok := s.core.summaryCache[sessionID]; ok && sum.size == info.Size() && sum.modified.Equal(info.ModTime()) {
		return sum, true
	}

	f, err := storage.OpenRegularFile(s.sessionPath(sessionID))
	if err != nil {
		return cachedSummary{}, false
	}
	defer f.Close()

	var startTime, firstTimestamp string
	var cwd, displayName, parentSession string
	var msgCount, contextMsgCount, totalTokens int
	hasConversation := false
	var previewBuilder, contextPreviewBuilder strings.Builder
	var previewRunes, contextPreviewRunes int
	var prompts []string
	var lastPrompt string

	scanner := newJSONLScanner(contextReader{ctx, f})
	lineNo := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return cachedSummary{}, false
		}
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
				if isConversationMessage(m) {
					hasConversation = true
				}
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

		if isConversationMessage(record.Message) {
			hasConversation = true
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

	if scanner.Err() != nil || ctx.Err() != nil {
		return cachedSummary{}, false
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
		hasVisible: hasConversation,
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
	return s.GetSessionsSortedContext(context.Background(), ascending)
}

// GetSessionsSortedContext allows cancellation while waiting for session storage.
func (s *JSONLStore) GetSessionsSortedContext(ctx context.Context, ascending bool) ([]session.SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.checkReady(""); err != nil {
		return nil, err
	}

	release, err := s.lockStore(ctx, false)
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sessionID, ok := validSessionEntry(entry)
		if !ok {
			continue
		}
		seen[sessionID] = true
		sum, ok := s.summarizeSessionLockedContext(ctx, sessionID, entry)
		if !ok || !sum.hasVisible {
			continue
		}
		sessions = append(sessions, sum.info)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
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
	return s.RenameSessionContext(context.Background(), oldID, newID)
}

// RenameSessionContext allows cancellation while waiting for session storage.
func (s *JSONLStore) RenameSessionContext(ctx context.Context, oldID, newID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := session.ValidateID(oldID); err != nil {
		return fmt.Errorf("invalid old session ID: %w", err)
	}
	if err := s.checkReady(newID); err != nil {
		return fmt.Errorf("invalid new session ID: %w", err)
	}

	release, err := s.lockStore(ctx, true)
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

	s.invalidateSessionCacheLocked(oldID)
	s.invalidateSessionCacheLocked(newID)
	latestPath := s.latestSessionPath()
	if data, err := storage.ReadFile(latestPath, 4096); err == nil && strings.TrimSpace(string(data)) == oldID {
		if err := s.writeLatestID(newID); err != nil {
			return fmt.Errorf("session renamed, but latest pointer update failed: %w", err)
		}
	}
	return nil
}

func readEntries(filePath string) ([]session.Record, error) {
	return readEntriesContext(context.Background(), filePath)
}

func readEntriesContext(ctx context.Context, filePath string) ([]session.Record, error) {
	f, err := storage.OpenRegularFile(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []session.Record
	scanner := newJSONLScanner(contextReader{ctx, f})
	line := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := session.ValidateEntries(entries); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *JSONLStore) Branch(sessionID, entryID string) (err error) {
	return s.BranchContext(context.Background(), sessionID, entryID)
}

// BranchContext allows cancellation while waiting for session storage.
func (s *JSONLStore) BranchContext(ctx context.Context, sessionID, entryID string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkReady(sessionID); err != nil {
		return err
	}

	release, err := s.lockStore(ctx, true)
	if err != nil {
		return err
	}
	defer release()

	filePath := s.sessionPath(sessionID)
	entries, err := readEntriesContext(ctx, filePath)
	if err != nil {
		return fmt.Errorf("failed to read session file: %w", err)
	}

	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return fmt.Errorf("entry %s not found in session %s", entryID, sessionID)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer f.Close()
	infoBefore, err := f.Stat()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			err = rollbackSessionAppend(f, infoBefore.Size(), false, err)
		}
	}()

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
	s.invalidateSessionCacheLocked(sessionID)
	if err := s.setLatestSessionIDUnlocked(sessionID); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *JSONLStore) ForkSession(parentSessionID, entryID string) (string, error) {
	return s.ForkSessionContext(context.Background(), parentSessionID, entryID)
}

// ForkSessionContext allows cancellation while waiting for session storage.
func (s *JSONLStore) ForkSessionContext(ctx context.Context, parentSessionID, entryID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := s.checkReady(parentSessionID); err != nil {
		return "", err
	}

	release, err := s.lockStore(ctx, true)
	if err != nil {
		return "", err
	}
	defer release()

	entries, err := readEntriesContext(ctx, s.sessionPath(parentSessionID))
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
		if err := ctx.Err(); err != nil {
			f.Close()
			return "", err
		}
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
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := os.Rename(f.Name(), s.sessionPath(newID)); err != nil {
		return "", fmt.Errorf("failed to finalize forked session file: %w", err)
	}
	return newID, nil
}

func ForkSession(parentSessionID, entryID string) (string, error) {
	return ForkSessionContext(context.Background(), parentSessionID, entryID)
}

func ForkSessionContext(ctx context.Context, parentSessionID, entryID string) (string, error) {
	return withStoreVal(func(s *JSONLStore) (string, error) { return s.ForkSessionContext(ctx, parentSessionID, entryID) })
}

func ClearHistory() error {
	return ClearHistoryContext(context.Background())
}

func ClearHistoryContext(ctx context.Context) error {
	return withStore(func(s *JSONLStore) error { return s.ClearHistoryContext(ctx) })
}

func ClearSession(sessionID string) error {
	return ClearSessionContext(context.Background(), sessionID)
}

func ClearSessionContext(ctx context.Context, sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.ClearSessionContext(ctx, sessionID) })
}

func SetLatestSessionID(sessionID string) error {
	return SetLatestSessionIDContext(context.Background(), sessionID)
}

func SetLatestSessionIDContext(ctx context.Context, sessionID string) error {
	return withStore(func(s *JSONLStore) error { return s.SetLatestSessionIDContext(ctx, sessionID) })
}

func GetSessions() ([]SessionInfo, error) {
	return GetSessionsContext(context.Background())
}

func GetSessionsContext(ctx context.Context) ([]SessionInfo, error) {
	return withStoreVal(func(s *JSONLStore) ([]SessionInfo, error) { return s.GetSessionsContext(ctx) })
}

func Branch(sessionID, entryID string) error {
	return BranchContext(context.Background(), sessionID, entryID)
}

func BranchContext(ctx context.Context, sessionID, entryID string) error {
	return withStore(func(s *JSONLStore) error { return s.BranchContext(ctx, sessionID, entryID) })
}

func Entries(sessionID string) ([]session.Record, error) {
	return EntriesContext(context.Background(), sessionID)
}

func EntriesContext(ctx context.Context, sessionID string) ([]session.Record, error) {
	return withStoreVal(func(s *JSONLStore) ([]session.Record, error) { return s.EntriesContext(ctx, sessionID) })
}

func RenameSession(oldID, newID string) error {
	return RenameSessionContext(context.Background(), oldID, newID)
}

func RenameSessionContext(ctx context.Context, oldID, newID string) error {
	return withStore(func(s *JSONLStore) error { return s.RenameSessionContext(ctx, oldID, newID) })
}

func RewriteSession(sessionID string, messages []message.Message) error {
	return RewriteSessionContext(context.Background(), sessionID, messages)
}

func RewriteSessionContext(ctx context.Context, sessionID string, messages []message.Message) error {
	return withStore(func(s *JSONLStore) error { return s.RewriteSessionContext(ctx, sessionID, messages) })
}

// A stored system prompt or settings record is not a conversation.
func isConversationMessage(msg message.Message) bool {
	return !isSkippedSlashRecord(msg) && msg.HasConversationContent()
}
