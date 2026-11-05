package file

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaintool "loop/pkg/domain/tool"
)

type mockAgentContext struct {
	workspaceRoot string
}

func (m *mockAgentContext) SafePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(m.workspaceRoot, p), nil
}

func (m *mockAgentContext) GetWorkspaceRoot() string {
	return m.workspaceRoot
}

func (m *mockAgentContext) GetActiveSkills() []domaintool.Skill {
	return nil
}

func (m *mockAgentContext) ReloadSkills() []domaintool.Skill {
	return nil
}

func (m *mockAgentContext) SpawnTask(command string, w io.Writer) (string, error) {
	return "", nil
}

func (m *mockAgentContext) GetTaskStatus(taskID string) (string, string, error) {
	return "", "", nil
}

func (m *mockAgentContext) KillTask(taskID string) error {
	return nil
}

func (m *mockAgentContext) Context() context.Context {
	return context.Background()
}

func (m *mockAgentContext) HasSubagent(name string) bool {
	return false
}

func TestReadToolExecuteResult_TextFile(t *testing.T) {
	tempDir := t.TempDir()
	textPath := filepath.Join(tempDir, "sample.txt")
	sampleContent := "line 1: hello\nline 2: world\nline 3: testing\n"
	if err := os.WriteFile(textPath, []byte(sampleContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	tool := NewReadTool()
	rich, ok := tool.(domaintool.ResultExecutor)
	if !ok {
		t.Fatalf("expected NewReadTool to implement ResultExecutor")
	}

	ctx := &mockAgentContext{workspaceRoot: tempDir}
	args := `{"path":"sample.txt"}`

	res, err := rich.ExecuteResult(ctx, args)
	if err != nil {
		t.Fatalf("ExecuteResult on text file failed: %v", err)
	}

	if res.IsError {
		t.Errorf("expected IsError to be false, got true")
	}

	if !strings.Contains(res.Output, "hello") || !strings.Contains(res.Output, "world") {
		t.Errorf("expected text output to contain 'hello' and 'world', got: %s", res.Output)
	}

	if len(res.Content) != 0 {
		t.Errorf("expected no image ContentBlocks for text file, got %d blocks", len(res.Content))
	}
}

func TestReadToolExecuteResult_ImageFile(t *testing.T) {
	tempDir := t.TempDir()
	imgPath := filepath.Join(tempDir, "test.png")
	// 1x1 transparent PNG bytes
	pngBytes, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=")
	if err := os.WriteFile(imgPath, pngBytes, 0644); err != nil {
		t.Fatalf("failed to write image file: %v", err)
	}

	tool := NewReadTool()
	rich, ok := tool.(domaintool.ResultExecutor)
	if !ok {
		t.Fatalf("expected NewReadTool to implement ResultExecutor")
	}

	ctx := &mockAgentContext{workspaceRoot: tempDir}
	args := `{"path":"test.png"}`

	res, err := rich.ExecuteResult(ctx, args)
	if err != nil {
		t.Fatalf("ExecuteResult on image file failed: %v", err)
	}

	if res.IsError {
		t.Errorf("expected IsError to be false, got true")
	}

	if len(res.Content) != 1 {
		t.Fatalf("expected 1 ContentBlock for image, got %d", len(res.Content))
	}

	block := res.Content[0]
	if block.Type != "image" {
		t.Errorf("expected ContentBlock Type 'image', got %q", block.Type)
	}

	if block.MimeType != "image/png" {
		t.Errorf("expected MimeType 'image/png', got %q", block.MimeType)
	}

	if !strings.Contains(res.Output, "Read image file") {
		t.Errorf("expected Output to contain 'Read image file', got: %s", res.Output)
	}
}
