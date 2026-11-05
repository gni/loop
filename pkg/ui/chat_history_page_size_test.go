package ui

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
)

func TestScrollbackPopTailRows(t *testing.T) {
	sb, err := render.NewPagedScrollback(80, 24)
	if err != nil {
		t.Fatalf("NewPagedScrollback failed: %v", err)
	}
	defer sb.Close()

	// Insert 100 rows
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line %03d", i))
	}
	sb.InsertHistory(0, strings.Join(lines, "\n")+"\n")

	if count := sb.HistoryRowCount(); count < 100 {
		t.Fatalf("expected at least 100 rows, got %d", count)
	}

	initialCount := sb.HistoryRowCount()
	tail := sb.PopTailRows(40)
	if tail == nil {
		t.Fatalf("expected non-nil tail")
	}
	defer tail.Close()

	if tail.HistoryRowCount() != 40 {
		t.Fatalf("expected tail to have 40 rows, got %d", tail.HistoryRowCount())
	}
	if sb.HistoryRowCount() != initialCount-40 {
		t.Fatalf("expected sb to have %d rows, got %d", initialCount-40, sb.HistoryRowCount())
	}
}

type mockHistorySource struct {
	turns [][]db.Message
	idx   int
}

func (m *mockHistorySource) Previous() ([]db.Message, error) {
	if m.idx >= len(m.turns) {
		return nil, io.EOF
	}
	turn := m.turns[m.idx]
	m.idx++
	return turn, nil
}

func (m *mockHistorySource) Close() error {
	m.idx = len(m.turns)
	return nil
}

func (m *mockHistorySource) HasMore() bool {
	return m.idx < len(m.turns)
}

func TestChatHistoryPageSizeEnforcement(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HistoryPageSize = 10

	// Create a turn with multiple tool calls
	turn1 := []db.Message{
		{Role: "user", Content: "run big command"},
	}
	for i := 1; i <= 10; i++ {
		callID := fmt.Sprintf("call_%d", i)
		turn1 = append(turn1,
			db.Message{
				Role: "assistant",
				ToolCalls: []db.ToolCall{
					{
						ID:   callID,
						Type: "function",
						Function: db.ToolFunction{
							Name:      "bash",
							Arguments: fmt.Sprintf(`{"command":"echo step %d"}`, i),
						},
					},
				},
			},
			db.Message{
				Role:       "tool",
				Name:       "bash",
				ToolCallID: callID,
				Content:    fmt.Sprintf("step %d output", i),
			},
		)
	}

	mockSrc := &mockHistorySource{
		turns: [][]db.Message{turn1},
	}

	a := &agent.Agent{
		Config: cfg,
	}

	var buf bytes.Buffer
	kiReader := &interceptor.KeyInterceptorReader{
		W: &buf,
	}
	ppWriter := NewPromptPreservingWriter(&buf, 24)

	h := &ChatHistory{
		a:           a,
		reader:      kiReader,
		writer:      ppWriter,
		captureOnly: true,
		source:      mockSrc,
	}
	paged, err := render.NewPagedScrollback(80, 24)
	if err != nil {
		t.Fatalf("NewPagedScrollback failed: %v", err)
	}
	paged.SetPageSize(cfg.HistoryPageSize)
	h.buffer = paged
	defer h.Close()

	// Initial load should respect HistoryPageSize = 50
	TerminalMu.Lock()
	added := h.loadOlderLocked()
	TerminalMu.Unlock()

	if added > cfg.HistoryPageSize {
		t.Fatalf("loadOlderLocked added %d rows, which exceeds HistoryPageSize %d", added, cfg.HistoryPageSize)
	}

	// Pending rows should hold the remainder of the oversized turn
	if h.pendingOlderRows == nil || h.pendingOlderRows.HistoryRowCount() == 0 {
		t.Fatalf("expected pendingOlderRows to retain the rest of the turn, got nil or 0")
	}

	// hasOlderLocked should be true because there are pending rows
	TerminalMu.Lock()
	hasOlder := h.hasOlderLocked()
	TerminalMu.Unlock()
	if !hasOlder {
		t.Fatalf("expected hasOlderLocked to be true while pending rows exist")
	}

	// Load next slice
	TerminalMu.Lock()
	added2 := h.loadOlderLocked()
	TerminalMu.Unlock()

	if added2 > cfg.HistoryPageSize {
		t.Fatalf("second loadOlderLocked added %d rows, which exceeds HistoryPageSize %d", added2, cfg.HistoryPageSize)
	}
}

func TestChatHistoryPrefetchPaging(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.HistoryPageSize = 10

	turn1 := []db.Message{
		{Role: "user", Content: "turn 1 prompt"},
	}
	for i := 1; i <= 15; i++ {
		callID := fmt.Sprintf("call_%d", i)
		turn1 = append(turn1,
			db.Message{
				Role: "assistant",
				ToolCalls: []db.ToolCall{
					{
						ID:   callID,
						Type: "function",
						Function: db.ToolFunction{
							Name:      "bash",
							Arguments: fmt.Sprintf(`{"command":"echo step %d"}`, i),
						},
					},
				},
			},
			db.Message{
				Role:       "tool",
				Name:       "bash",
				ToolCallID: callID,
				Content:    fmt.Sprintf("step %d output", i),
			},
		)
	}

	mockSrc := &mockHistorySource{
		turns: [][]db.Message{turn1},
	}

	a := &agent.Agent{
		Config: cfg,
	}

	var buf bytes.Buffer
	kiReader := &interceptor.KeyInterceptorReader{
		W: &buf,
	}
	ppWriter := NewPromptPreservingWriter(&buf, 24)

	h := &ChatHistory{
		a:           a,
		reader:      kiReader,
		writer:      ppWriter,
		captureOnly: true,
		source:      mockSrc,
	}
	paged, err := render.NewPagedScrollback(80, 24)
	if err != nil {
		t.Fatalf("NewPagedScrollback failed: %v", err)
	}
	paged.SetPageSize(cfg.HistoryPageSize)
	h.buffer = paged
	defer h.Close()

	// Initial load
	TerminalMu.Lock()
	added := h.loadOlderLocked()
	TerminalMu.Unlock()

	if added > cfg.HistoryPageSize {
		t.Fatalf("initial load added %d rows, exceeded %d", added, cfg.HistoryPageSize)
	}

	// Prepare older page synchronously via prepareOlderPage
	job := &historyPageJob{stop: make(chan struct{})}
	newPage := h.buffer.NewHistoryPage()
	theme, appearance := GetConfiguredTheme(cfg), h.appearanceKey()

	TerminalMu.Lock()
	pending := h.pendingOlderRows
	h.pendingOlderRows = nil
	h.pageJob = job
	TerminalMu.Unlock()

	prepared := h.prepareOlderPage(job, h.source, newPage, pending, cfg, theme, appearance)
	if prepared.err != nil {
		t.Fatalf("prepareOlderPage failed: %v", prepared.err)
	}
	if prepared.rows.HistoryRowCount() > cfg.HistoryPageSize {
		t.Fatalf("prepared.rows has %d rows, exceeded HistoryPageSize %d", prepared.rows.HistoryRowCount(), cfg.HistoryPageSize)
	}

	TerminalMu.Lock()
	h.finishPreparedPageLocked(job, h.source, prepared)
	if h.readyPage == nil {
		TerminalMu.Unlock()
		t.Fatalf("expected readyPage to be set")
	}

	// Apply older page
	applied := h.applyOlderPageLocked()
	TerminalMu.Unlock()

	if applied > cfg.HistoryPageSize {
		t.Fatalf("applyOlderPageLocked added %d rows, exceeded HistoryPageSize %d", applied, cfg.HistoryPageSize)
	}
}
func TestChatHistoryCollapseToggle(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CollapseResults = false

	turn1 := []db.Message{
		{Role: "user", Content: "test command"},
		{
			Role: "assistant",
			ToolCalls: []db.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: db.ToolFunction{
						Name:      "bash",
						Arguments: `{"command":"echo test"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			Name:       "bash",
			ToolCallID: "call_1",
			Content:    "line 1\nline 2\nline 3\nline 4\nline 5",
		},
	}

	mockSrc := &mockHistorySource{
		turns: [][]db.Message{turn1},
	}

	a := &agent.Agent{
		Config: cfg,
	}

	var buf bytes.Buffer
	kiReader := &interceptor.KeyInterceptorReader{
		W:     &buf,
		Agent: a,
	}
	ppWriter := NewPromptPreservingWriter(&buf, 24)

	h := &ChatHistory{
		a:           a,
		reader:      kiReader,
		writer:      ppWriter,
		captureOnly: true,
		source:      mockSrc,
	}
	paged, err := render.NewPagedScrollback(80, 24)
	if err != nil {
		t.Fatalf("NewPagedScrollback failed: %v", err)
	}
	h.buffer = paged
	defer h.Close()

	h.PrintInitialHistory()
	linesUncollapsed := h.buffer.Base() + h.buffer.Height
	t.Logf("Uncollapsed lines: %d, turns=%d", linesUncollapsed, len(h.turns))
	if len(h.turns) > 0 {
		t.Logf("Turn 0: start=%d, end=%d, appearance=%s, size=%d, msgs=%d",
			h.turns[0].start, h.turns[0].end, h.turns[0].appearance, h.turns[0].size, len(h.turns[0].messages))
	}

	// Toggle collapse to true
	cfg.CollapseResults = true
	h.RepaintTurns()
	linesCollapsed := h.buffer.Base() + h.buffer.Height
	t.Logf("Collapsed lines: %d", linesCollapsed)
	if len(h.turns) > 0 {
		t.Logf("Turn 0 after collapse: start=%d, end=%d, appearance=%s",
			h.turns[0].start, h.turns[0].end, h.turns[0].appearance)
	}

	// Toggle back to false
	cfg.CollapseResults = false
	h.RepaintTurns()
	linesExpanded := h.buffer.Base() + h.buffer.Height
	t.Logf("Expanded lines again: %d", linesExpanded)
	if len(h.turns) > 0 {
		t.Logf("Turn 0 after expand: start=%d, end=%d, appearance=%s",
			h.turns[0].start, h.turns[0].end, h.turns[0].appearance)
	}
}
