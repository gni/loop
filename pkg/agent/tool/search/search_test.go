package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaintool "loop/pkg/domain/tool"
)

type mockAgentContext struct {
	domaintool.AgentContext
	workspaceRoot string
	ctx           context.Context
}

func (m *mockAgentContext) Context() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}
func (m *mockAgentContext) GetWorkspaceRoot() string { return m.workspaceRoot }
func (m *mockAgentContext) SafePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	return filepath.Join(m.workspaceRoot, p), nil
}

func TestSearchIgnoreHelpers(t *testing.T) {
	if !IsIgnoredDirName(".git") {
		t.Errorf("expected .git to be ignored")
	}
	if !IsIgnoredDirName("node_modules") {
		t.Errorf("expected node_modules to be ignored")
	}
	if IsIgnoredDirName("pkg") {
		t.Errorf("expected pkg not to be ignored")
	}

	if !IsIgnoredFileName("loop.exe") {
		t.Errorf("expected .exe to be ignored")
	}
	if !IsIgnoredFileName("data.sqlite") {
		t.Errorf("expected .sqlite to be ignored")
	}
	if IsIgnoredFileName("main.go") {
		t.Errorf("expected main.go not to be ignored")
	}

	if !IsBinary([]byte("abc\x00def")) {
		t.Errorf("expected data with null byte to be binary")
	}
	if IsBinary([]byte("hello world\n")) {
		t.Errorf("expected text not to be binary")
	}
}

func TestGrepFindListSubpackage(t *testing.T) {
	tmpDir := t.TempDir()

	file1 := filepath.Join(tmpDir, "hello.txt")
	_ = os.WriteFile(file1, []byte("Hello World!\nFoo Bar\nLoop Engine\n"), 0644)

	subDir := filepath.Join(tmpDir, "sub")
	_ = os.Mkdir(subDir, 0755)
	file2 := filepath.Join(subDir, "code.go")
	_ = os.WriteFile(file2, []byte("package sub\nfunc HelloWorld() {}\n"), 0644)

	ctx := &mockAgentContext{workspaceRoot: tmpDir}

	grep := NewGrepTool()
	out, err := grep.Execute(ctx, `{"pattern": "World", "path": "."}`)
	if err != nil {
		t.Fatalf("grep failed: %v", err)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Errorf("expected grep output to contain hello.txt, got:\n%s", out)
	}

	find := NewFindTool()
	findOut, err := find.Execute(ctx, `{"pattern": "*.go"}`)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if !strings.Contains(findOut, "code.go") {
		t.Errorf("expected find output to contain code.go, got:\n%s", findOut)
	}

	list := NewListTool()
	listOut, err := list.Execute(ctx, `{"path": "."}`)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(listOut, "hello.txt") || !strings.Contains(listOut, "sub/") {
		t.Errorf("expected list output to contain hello.txt and sub/, got:\n%s", listOut)
	}
}
