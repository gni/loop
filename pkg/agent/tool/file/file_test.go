package file

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaintool "loop/pkg/domain/tool"
)

type dummyAgentContext struct {
	root string
}

func (c *dummyAgentContext) SafePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(c.root, p), nil
}
func (c *dummyAgentContext) GetWorkspaceRoot() string                    { return c.root }
func (c *dummyAgentContext) GetActiveSkills() []domaintool.Skill         { return nil }
func (c *dummyAgentContext) ReloadSkills() []domaintool.Skill            { return nil }
func (c *dummyAgentContext) SpawnTask(string, io.Writer) (string, error) { return "", nil }

func (c *dummyAgentContext) GetTaskStatus(string) (string, string, error) { return "", "", nil }
func (c *dummyAgentContext) KillTask(string) error                        { return nil }
func (c *dummyAgentContext) Context() context.Context                     { return context.Background() }
func (c *dummyAgentContext) HasSubagent(string) bool                      { return false }

func TestFileSubpackageReadWriteEdit(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &dummyAgentContext{root: tmpDir}

	writeT := NewWriteTool()
	readT := NewReadTool()
	editT := NewEditTool()

	// 1. Write file
	_, err := writeT.Execute(ctx, `{"path":"example.txt","content":"line 1\nline 2\nline 3\n"}`)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// 2. Read file
	readOut, err := readT.Execute(ctx, `{"path":"example.txt"}`)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !strings.Contains(readOut, "line 2") {
		t.Fatalf("expected line 2 in read output: %s", readOut)
	}

	// 3. Edit file
	editOut, err := editT.Execute(ctx, `{"path":"example.txt","updates":[{"oldText":"line 2","newText":"updated line 2"}]}`)
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	if !strings.Contains(editOut, "updated line 2") {
		t.Fatalf("expected diff output to reflect changes: %s", editOut)
	}

	// 4. Verify file content on disk
	content, err := os.ReadFile(filepath.Join(tmpDir, "example.txt"))
	if err != nil {
		t.Fatalf("read back failed: %v", err)
	}
	expected := "line 1\nupdated line 2\nline 3\n"
	if string(content) != expected {
		t.Fatalf("expected %q, got %q", expected, string(content))
	}
}

func TestMyersDiff(t *testing.T) {
	a := []string{"apple", "banana", "cherry"}
	b := []string{"apple", "blueberry", "cherry"}

	parts := computeMyersDiff(a, b)
	if len(parts) == 0 {
		t.Fatalf("expected diff parts, got none")
	}

	diffStr := GenerateDisplayDiff(strings.Join(a, "\n"), strings.Join(b, "\n"), 1)
	if !strings.Contains(diffStr, "- banana") || !strings.Contains(diffStr, "+ blueberry") {
		t.Fatalf("unexpected diff display: %s", diffStr)
	}
}

func TestFuzzyMatchingTiers(t *testing.T) {
	content := "function calculateTotal(items) {\n    return items.reduce((a, b) => a + b, 0);\n}\n"
	oldText := "return items.reduce((a, b) => a + b, 0);"
	newText := "return items.reduce((acc, curr) => acc + curr, 0);"

	m, err := locateEditMatch(content, oldText, newText)
	if err != nil {
		t.Fatalf("locateEditMatch failed: %v", err)
	}
	if m == nil {
		t.Fatalf("expected match, got nil")
	}
	if m.matchedText != oldText {
		t.Fatalf("expected matched text %q, got %q", oldText, m.matchedText)
	}
}
