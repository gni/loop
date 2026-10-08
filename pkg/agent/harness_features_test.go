package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type mockHarnessProvider struct {
	responses []*db.Message
	callIndex int
}

func (p *mockHarnessProvider) StreamChatCompletions(
	ctx context.Context,
	messages []db.Message,
	tools []Tool,
	chunkChan chan<- StreamChunk,
) (*db.Message, error) {
	if p.callIndex < len(p.responses) {
		resp := p.responses[p.callIndex]
		p.callIndex++
		return resp, nil
	}
	return &db.Message{Role: "assistant", Content: "Done"}, nil
}

func (p *mockHarnessProvider) CheckThinkingSupport(ctx context.Context) bool {
	return false
}

func TestHarnessFeaturesIntegration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "loop-harness-feat-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := config.DefaultConfig()
	cfg.MaxToolOutputBytes = 200 // small threshold to trigger spill
	a := NewAgent(cfg, "", nil)
	a.WorkspaceRoot = tmpDir

	// 1. Test todo tool via Agent.Registry
	todoArgs := `{"tasks": [{"id": "t1", "task": "Initial setup", "status": "in_progress"}]}`
	todoOut, err := a.Registry.Execute(a, "todo", todoArgs)
	if err != nil {
		t.Fatalf("unexpected error executing todo: %v", err)
	}
	if !strings.Contains(todoOut, "[>] t1: Initial setup") {
		t.Errorf("expected in_progress marker, got: %s", todoOut)
	}
	todos := a.GetTodos()
	if len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Errorf("expected stored todos to match, got: %+v", todos)
	}

	// 2. Test File Observation (Read-Before-Edit CAS)
	testFile := filepath.Join(tmpDir, "code.go")
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc Run() int { return 1 }\n"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Attempt edit WITHOUT prior read -> MUST be rejected
	editArgs := `{"path": "code.go", "updates": [{"oldText": "return 1", "newText": "return 2"}]}`
	_, err = a.Registry.Execute(a, "edit", editArgs)
	if err == nil {
		t.Errorf("expected edit on unread file to fail")
	}
	if !strings.Contains(err.Error(), "has not been read in this session") {
		t.Errorf("expected unread error message, got: %v", err)
	}

	// Now read the file
	readArgs := `{"path": "code.go"}`
	readOut, err := a.Registry.Execute(a, "read", readArgs)
	if err != nil {
		t.Fatalf("unexpected read error: %v", err)
	}
	if !strings.Contains(readOut, "func Run()") {
		t.Errorf("expected read output, got: %s", readOut)
	}

	// Now edit should succeed
	editOut, err := a.Registry.Execute(a, "edit", editArgs)
	if err != nil {
		t.Fatalf("unexpected edit error after read: %v", err)
	}
	if !strings.Contains(editOut, "return 2") {
		t.Errorf("expected edit diff output, got: %s", editOut)
	}

	// External modification on disk -> CAS failure
	if err := os.WriteFile(testFile, []byte("package main\n\nfunc Run() int { return 999 }\n"), 0644); err != nil {
		t.Fatalf("failed to write external modification: %v", err)
	}
	_, err = a.Registry.Execute(a, "edit", editArgs)
	if err == nil {
		t.Errorf("expected CAS failure after external modification")
	}
	if !strings.Contains(err.Error(), "modified on disk since your last read") {
		t.Errorf("expected stale version error message, got: %v", err)
	}

	// 3. Test KV-Cache Prefix Preservation across turns in RunAgentLoop
	mockProvider := &mockHarnessProvider{
		responses: []*db.Message{
			{Role: "assistant", Content: "First answer"},
			{Role: "assistant", Content: "Second answer"},
		},
	}
	a.LLMProvider = mockProvider

	messages := []db.Message{
		{Role: "system", Content: a.GetSystemPrompt()},
	}
	initialSysPrompt := messages[0].Content

	theme := style.UITheme{}
	// Turn 1
	a.RunAgentLoop(context.Background(), io.Discard, &messages, "First turn", nil, theme, true, "sess-test")
	if messages[0].Content != initialSysPrompt {
		t.Errorf("system prompt was mutated on turn 1")
	}

	// Turn 2
	a.RunAgentLoop(context.Background(), io.Discard, &messages, "Second turn", nil, theme, true, "sess-test")
	if messages[0].Content != initialSysPrompt {
		t.Errorf("system prompt was mutated on turn 2 (KV-cache invalidated!)")
	}
}
