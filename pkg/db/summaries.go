package db

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strconv"
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
	for field := range strings.FieldsSeq(text) {
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

func (s *JSONLStore) summarizeSessionLockedContext(ctx context.Context, sessionID string, entry os.DirEntry) (cachedSummary, bool) {
	if ctx.Err() != nil {
		return cachedSummary{}, false
	}
	info, err := entry.Info()
	if err != nil {
		return cachedSummary{}, false
	}
	if sum, ok := s.core.summaryCache[sessionID]; ok && sum.matches(info) {
		s.core.cacheClock++
		sum.used = s.core.cacheClock
		s.core.summaryCache[sessionID] = sum
		return sum, true
	}

	f, err := storage.OpenRegularFile(s.sessionPath(sessionID))
	if err != nil {
		return cachedSummary{}, false
	}
	defer f.Close()

	var startTime, firstTimestamp string
	var cwd, displayName, parentSession string

	type summaryNode struct {
		id              string
		parentID        string
		recordType      string
		timestamp       string
		resetHistory    bool
		msgCount        int
		tokens          int
		hasConversation bool
		preview         string
		prompts         []string
	}

	extractPrompt := func(msg message.Message) (string, bool) {
		const manualPrefix = "[user manually executed"
		if !msg.IsUserInput() || msg.IsShellContext() || len(msg.Content) >= len(manualPrefix) && strings.EqualFold(msg.Content[:len(manualPrefix)], manualPrefix) {
			return "", false
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			return "", false
		}
		return text, true
	}

	var nodes []summaryNode
	ids := make(map[string]int)
	prevID := ""

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
		}

		node := summaryNode{
			id:           record.ID,
			parentID:     record.ParentID,
			recordType:   record.Type,
			timestamp:    record.Timestamp,
			resetHistory: record.ResetHistory,
		}
		if node.id == "" {
			node.id = "legacy-" + strconv.Itoa(len(nodes))
			node.parentID = prevID
			if node.recordType == "" {
				node.recordType = "message"
			}
		}

		switch record.Type {
		case "context", "compaction":
			var ctxPreview strings.Builder
			var ctxPreviewRunes int
			var ctxPrompts []string
			seenCtx := make(map[string]bool)
			for _, m := range record.Messages {
				if isSkippedSlashRecord(m) || m.Role == "" || m.IsInternalContext() {
					continue
				}
				node.msgCount++
				node.tokens += m.PromptTokens + m.CompletionTokens
				if p, ok := extractPrompt(m); ok && !seenCtx[p] {
					seenCtx[p] = true
					ctxPrompts = append(ctxPrompts, p)
				}
				if isConversationMessage(m) {
					node.hasConversation = true
				}
				if m.Content != "" {
					appendPreviewSnippet(&ctxPreview, &ctxPreviewRunes, m.Content)
				}
			}
			node.preview = ctxPreview.String()
			node.prompts = ctxPrompts
		case "", "message":
			if record.Message.Role != "" && !isSkippedSlashRecord(record.Message) && !record.Message.IsInternalContext() {
				node.msgCount = 1
				node.hasConversation = isConversationMessage(record.Message)
				node.tokens = record.Message.PromptTokens + record.Message.CompletionTokens
				if record.Message.Content != "" {
					var b strings.Builder
					var runes int
					appendPreviewSnippet(&b, &runes, record.Message.Content)
					node.preview = b.String()
				}
				if p, ok := extractPrompt(record.Message); ok {
					node.prompts = []string{p}
				}
			}
		}

		ids[node.id] = len(nodes)
		nodes = append(nodes, node)
		if record.Type != "branch" {
			prevID = node.id
		}
	}

	if scanner.Err() != nil || ctx.Err() != nil {
		return cachedSummary{}, false
	}

	// Walk parent pointers from the active leaf back to the root, matching session.LeafPath.
	var reverse []int
	if len(nodes) > 0 {
		seenNode := make(map[int]bool, len(nodes))
		for cur := len(nodes) - 1; cur >= 0; {
			if seenNode[cur] {
				reverse = nil
				break
			}
			seenNode[cur] = true
			reverse = append(reverse, cur)
			parent := nodes[cur].parentID
			if parent == "" {
				break
			}
			next, ok := ids[parent]
			if !ok {
				reverse = nil
				break
			}
			cur = next
		}
	}

	var msgCount, baselineCount, totalTokens int
	var baselineKnown bool
	hasConversation := false
	var previewBuilder strings.Builder
	var previewRunes int
	var prompts []string
	var lastPrompt string
	seenPrompts := make(map[string]bool)
	addPrompt := func(text string, fromContext bool) {
		if text == "" || text == lastPrompt || (fromContext && seenPrompts[text]) {
			return
		}
		prompts = append(prompts, text)
		seenPrompts[text] = true
		lastPrompt = text
	}

	for i := len(reverse) - 1; i >= 0; i-- {
		n := nodes[reverse[i]]
		switch n.recordType {
		case "context", "compaction":
			if (msgCount == 0 && !baselineKnown) || n.resetHistory {
				baselineCount, msgCount, baselineKnown = n.msgCount, 0, true
				hasConversation = n.hasConversation
				previewBuilder.Reset()
				previewRunes = 0
				appendPreviewSnippet(&previewBuilder, &previewRunes, n.preview)
				if n.resetHistory {
					totalTokens = n.tokens
					prompts = nil
					lastPrompt = ""
					clear(seenPrompts)
				}
				for _, p := range n.prompts {
					addPrompt(p, true)
				}
			} else {
				for _, p := range n.prompts {
					addPrompt(p, true)
				}
			}
			if firstTimestamp == "" && n.timestamp != "" {
				firstTimestamp = n.timestamp
			}
		case "", "message":
			if n.msgCount == 0 {
				continue
			}
			if n.hasConversation {
				hasConversation = true
			}
			msgCount += n.msgCount
			if firstTimestamp == "" && n.timestamp != "" {
				firstTimestamp = n.timestamp
			}
			totalTokens += n.tokens
			if n.preview != "" {
				appendPreviewSnippet(&previewBuilder, &previewRunes, n.preview)
			}
			for _, p := range n.prompts {
				addPrompt(p, false)
			}
		}
	}

	effectiveCount := baselineCount + msgCount
	rawPreview := previewBuilder.String()

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
		cachedFile: cacheFile(info),
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
	s.cacheSummaryLocked(sessionID, sum)
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
