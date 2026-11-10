package db

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	"loop/pkg/storage"
)

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
