package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	filetool "loop/pkg/agent/tool/file"
)

type fileTestContext struct {
	root string
}

func (c *fileTestContext) SafePath(inputPath string) (string, error) {
	if filepath.IsAbs(inputPath) {
		return inputPath, nil
	}
	return filepath.Join(c.root, inputPath), nil
}

func (c *fileTestContext) GetWorkspaceRoot() string {
	return c.root
}

func (c *fileTestContext) GetActiveSkills() []Skill {
	return nil
}

func (c *fileTestContext) ReloadSkills() []Skill {
	return nil
}

func (c *fileTestContext) SpawnTask(string, io.Writer) (string, error) {
	return "", fmt.Errorf("not implemented")
}

func (c *fileTestContext) GetTaskStatus(string) (string, string, error) {
	return "", "", fmt.Errorf("not implemented")
}

func (c *fileTestContext) KillTask(string) error {
	return fmt.Errorf("not implemented")
}

func (c *fileTestContext) Context() context.Context {
	return context.Background()
}

func (c *fileTestContext) HasSubagent(string) bool {
	return false
}

func TestBOMHandling(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "bom.txt")
	ctx := &fileTestContext{root: tmpDir}

	// Write file with UTF-8 BOM
	rawWithBOM := append([]byte{0xef, 0xbb, 0xbf}, []byte("hello world\nline 2")...)
	if err := os.WriteFile(filePath, rawWithBOM, 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 1. Test read strips BOM
	readTool := NewReadTool()
	out, err := readTool.Execute(ctx, `{"path":"bom.txt"}`)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if strings.Contains(out, "\xef\xbb\xbf") {
		t.Errorf("read output should not contain raw BOM bytes")
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("read output missing content: %q", out)
	}

	// 2. Test edit preserves BOM
	editTool := NewEditTool()
	_, err = editTool.Execute(ctx, `{"path":"bom.txt","updates":[{"oldText":"hello world","newText":"greetings universe"}]}`)
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	saved, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}
	if len(saved) < 3 || saved[0] != 0xef || saved[1] != 0xbb || saved[2] != 0xbf {
		t.Errorf("file after edit lost its UTF-8 BOM header")
	}
	if !strings.Contains(string(saved), "greetings universe") {
		t.Errorf("file missing updated text: %q", string(saved))
	}
}

func TestCRLFPreservation(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "crlf.txt")
	ctx := &fileTestContext{root: tmpDir}

	crlfContent := "first line\r\nsecond line\r\nthird line\r\n"
	if err := os.WriteFile(filePath, []byte(crlfContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	editTool := NewEditTool()
	_, err := editTool.Execute(ctx, `{"path":"crlf.txt","updates":[{"oldText":"second line","newText":"modified line"}]}`)
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	saved, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}

	if !strings.Contains(string(saved), "\r\n") {
		t.Errorf("edited file should preserve Windows CRLF line endings")
	}
	if !strings.Contains(string(saved), "modified line\r\n") {
		t.Errorf("edited content line does not end with CRLF: %q", string(saved))
	}
}

func TestUnicodeFuzzyMatchInEdit(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "code.py")
	ctx := &fileTestContext{root: tmpDir}

	// File has standard ASCII quotes and hyphens
	content := "title = \"User Profile\" - v1\nname = 'John'\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	editTool := NewEditTool()
	// Model sends smart quotes and em-dash instead of ASCII
	_, err := editTool.Execute(ctx, `{"path":"code.py","updates":[{"oldText":"title = “User Profile” — v1","newText":"title = \"Member Profile\" - v2"}]}`)
	if err != nil {
		t.Fatalf("edit with smart quotes and em-dash failed to match: %v", err)
	}

	saved, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read back: %v", err)
	}

	if !strings.Contains(string(saved), "Member Profile") {
		t.Errorf("file missing replacement: %q", string(saved))
	}
}

func TestPromptContributors(t *testing.T) {
	tools := []ToolExecutor{
		NewReadTool(),
		NewWriteTool(),
		NewEditTool(),
		NewBashTool(),
		NewGrepTool(),
		NewFindTool(),
		NewListTool(),
	}

	for _, tool := range tools {
		snippet := GetPromptSnippet(tool)
		if snippet == "" {
			t.Errorf("tool %s returned empty PromptSnippet", tool.Name())
		}
	}
}

func TestEditDiffOnlyMarksChangedLines(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "app.py")
	ctx := &fileTestContext{root: tmpDir}

	content := `from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from src.config.settings import settings
from src.modules.items.presentation.controllers.item_controller import router as items_router
from src.core.middleware.auth import auth_middleware

app = FastAPI(title=settings.app_name, version=settings.app_version)

app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_credentials=True, allow_methods=["*"], allow_headers=["*"])

@app.middleware("http")
async def auth(request, call_next):
    return await auth_middleware(request, call_next)

@app.get("/health")
async def health():
    return {"status": "ok"}

app.include_router(items_router)`

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	editTool := NewEditTool()

	newContent := `from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from src.config.settings import settings
from src.modules.items.presentation.controllers.item_controller import router as items_router
from src.modules.cats.presentation.controllers.cat_controller import router as cats_router
from src.core.middleware.auth import auth_middleware

app = FastAPI(title=settings.app_name, version=settings.app_version)

app.add_middleware(CORSMiddleware, allow_origins=["*"], allow_credentials=True, allow_methods=["*"], allow_headers=["*"])

@app.middleware("http")
async def auth(request, call_next):
    return await auth_middleware(request, call_next)

@app.get("/health")
async def health():
    return {"status": "ok"}

app.include_router(items_router)
app.include_router(cats_router)`

	payload, err := json.Marshal(map[string]interface{}{
		"path": "app.py",
		"updates": []map[string]string{
			{"oldText": content, "newText": newContent},
		},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	diffOutput, err := editTool.Execute(ctx, string(payload))
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}

	if !strings.Contains(diffOutput, "+ from src.modules.cats") {
		t.Errorf("expected added cats router import in diff: %s", diffOutput)
	}
	if !strings.Contains(diffOutput, "+ app.include_router(cats_router)") {
		t.Errorf("expected added include_router in diff: %s", diffOutput)
	}

	for _, line := range strings.Split(diffOutput, "\n") {
		if strings.Contains(line, "from fastapi import FastAPI") {
			if strings.Contains(line, "-") || strings.Contains(line, "+") {
				t.Errorf("unchanged line should not have - or +: %s", line)
			}
		}
	}

	if !strings.Contains(diffOutput, "...") {
		t.Errorf("expected large unchanged region to be collapsed with '...': %s", diffOutput)
	}
}

func TestReadResolvesDirectoryEntrypoint(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &fileTestContext{root: tmpDir}

	pkgDir := filepath.Join(tmpDir, "src", "core", "errors")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}
	initFile := filepath.Join(pkgDir, "__init__.py")
	initContent := "class DomainException(Exception):\n    pass\n"
	if err := os.WriteFile(initFile, []byte(initContent), 0644); err != nil {
		t.Fatalf("failed to write __init__.py: %v", err)
	}

	readTool := NewReadTool()

	out, err := readTool.Execute(ctx, `{"path":"src/core/errors"}`)
	if err != nil {
		t.Fatalf("read directory failed: %v", err)
	}
	if !strings.Contains(out, "DomainException") {
		t.Fatalf("read directory should have loaded __init__.py content, got: %s", out)
	}
	if !strings.Contains(out, "Automatically reading package entrypoint") {
		t.Fatalf("read directory output missing entrypoint notice: %s", out)
	}

	out2, err := readTool.Execute(ctx, `{"path":"src/core/errors.py"}`)
	if err != nil {
		t.Fatalf("read directory.py failed: %v", err)
	}
	if !strings.Contains(out2, "DomainException") {
		t.Fatalf("read directory.py should have loaded __init__.py content, got: %s", out2)
	}

	dbFile := filepath.Join(tmpDir, "src", "core", "database.py")
	if err := os.WriteFile(dbFile, []byte("def get_engine(): pass\n"), 0644); err != nil {
		t.Fatalf("failed to write database.py: %v", err)
	}
	out3, err := readTool.Execute(ctx, `{"path":"src/core/database"}`)
	if err != nil {
		t.Fatalf("read file without extension failed: %v", err)
	}
	if !strings.Contains(out3, "get_engine") {
		t.Fatalf("read file without extension should have resolved to database.py, got: %s", out3)
	}
}

func TestToolFloatArgumentsUnmarshaling(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &fileTestContext{root: tmpDir}

	sampleFile := filepath.Join(tmpDir, "sample.txt")
	sampleContent := "line 1\nline 2: target match\nline 3\nline 4\n"
	if err := os.WriteFile(sampleFile, []byte(sampleContent), 0644); err != nil {
		t.Fatalf("failed to write sample.txt: %v", err)
	}

	// 1. Test read with float offset and limit
	readTool := NewReadTool()
	readOut, err := readTool.Execute(ctx, `{"path": "sample.txt", "offset": 2.0, "limit": 2.0}`)
	if err != nil {
		t.Fatalf("read with float offset/limit failed: %v", err)
	}
	if !strings.Contains(readOut, "line 2: target match") {
		t.Fatalf("read output unexpected: %s", readOut)
	}

	// 2. Test find with float limit
	findTool := NewFindTool()
	findOut, err := findTool.Execute(ctx, `{"pattern": "*.txt", "limit": 500.0}`)
	if err != nil {
		t.Fatalf("find with float limit failed: %v", err)
	}
	if !strings.Contains(findOut, "sample.txt") {
		t.Fatalf("find output unexpected: %s", findOut)
	}

	// 3. Test grep with float limit and context
	grepTool := NewGrepTool()
	grepOut, err := grepTool.Execute(ctx, `{"pattern": "target", "limit": 100.0, "context": 1.0}`)
	if err != nil {
		t.Fatalf("grep with float limit/context failed: %v", err)
	}
	if !strings.Contains(grepOut, "line 2: target match") {
		t.Fatalf("grep output unexpected: %s", grepOut)
	}

	// 4. Test list with float depth
	listTool := NewListTool()
	listOut, err := listTool.Execute(ctx, `{"path": ".", "depth": 2.0}`)
	if err != nil {
		t.Fatalf("list with float depth failed: %v", err)
	}
	if !strings.Contains(listOut, "sample.txt") {
		t.Fatalf("list output unexpected: %s", listOut)
	}
}

func TestWriteToolAliasesAndNormalizeName(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &fileTestContext{root: tmpDir}

	// 1. Verify NormalizeName mappings
	tests := []struct {
		input    string
		expected string
	}{
		{"write_path", "write"},
		{"write_file", "write"},
		{"writeFile", "write"},
		{"create_file", "write"},
		{"write_to_file", "write"},
		{"edit_file", "edit"},
		{"editFile", "edit"},
		{"read_file", "read"},
		{"readFile", "read"},
		{"list_dir", "list"},
		{"ls", "list"},
		{"run_command", "bash"},
		{"exec", "bash"},
		{"shell", "bash"},
		{"find_files", "find"},
		{"find_file", "find"},
	}

	for _, tc := range tests {
		if got := NormalizeName(tc.input); got != tc.expected {
			t.Errorf("NormalizeName(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}

	// 2. Test ToolRegistry execution via alias "write_path"
	r := NewToolRegistry()
	r.Register(NewWriteTool())

	_, err := r.Execute(ctx, "write_path", `{"path": "from_write_path.txt", "write_content": "created via write_path"}`)
	if err != nil {
		t.Fatalf("r.Execute with write_path failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "from_write_path.txt"))
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(data) != "created via write_path" {
		t.Fatalf("file content mismatch: got %q", string(data))
	}

	// 3. Test writeTool argument aliases (e.g. write_path and target_file as path key)
	writeTool := NewWriteTool()
	_, err = writeTool.Execute(ctx, `{"write_path": "alias_key.txt", "content": "hello alias key"}`)
	if err != nil {
		t.Fatalf("writeTool.Execute with write_path key failed: %v", err)
	}

	data2, err := os.ReadFile(filepath.Join(tmpDir, "alias_key.txt"))
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	if string(data2) != "hello alias key" {
		t.Fatalf("file content mismatch: got %q", string(data2))
	}
}

func TestEditToolPathAndFormatAliases(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &fileTestContext{root: tmpDir}
	editTool := NewEditTool()

	initialContent := "func alpha() string {\n\treturn \"alpha\"\n}\n"
	testFile := filepath.Join(tmpDir, "sample.go")
	if err := os.WriteFile(testFile, []byte(initialContent), 0644); err != nil {
		t.Fatalf("failed to create initial file: %v", err)
	}

	// 1. target_file alias with standard updates
	_, err := editTool.Execute(ctx, `{"target_file": "sample.go", "updates": [{"oldText": "return \"alpha\"", "newText": "return \"beta\""}]}`)
	if err != nil {
		t.Fatalf("edit with target_file failed: %v", err)
	}
	data, _ := os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"beta\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 2. TargetFile pascal-case alias
	_, err = editTool.Execute(ctx, `{"TargetFile": "sample.go", "updates": [{"oldText": "return \"beta\"", "newText": "return \"gamma\""}]}`)
	if err != nil {
		t.Fatalf("edit with TargetFile failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"gamma\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 3. filePath camelCase alias
	_, err = editTool.Execute(ctx, `{"filePath": "sample.go", "updates": [{"oldText": "return \"gamma\"", "newText": "return \"delta\""}]}`)
	if err != nil {
		t.Fatalf("edit with filePath failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"delta\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 4. Nested wrapper {"input": {"path": "...", "updates": [...]}}
	_, err = editTool.Execute(ctx, `{"input": {"path": "sample.go", "updates": [{"oldText": "return \"delta\"", "newText": "return \"epsilon\""}]}}`)
	if err != nil {
		t.Fatalf("edit with nested input failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"epsilon\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 5. Path inside updates[0]
	_, err = editTool.Execute(ctx, `{"updates": [{"path": "sample.go", "oldText": "return \"epsilon\"", "newText": "return \"zeta\""}]}`)
	if err != nil {
		t.Fatalf("edit with path inside updates failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"zeta\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 6. Top-level single edit with TargetContent and ReplacementContent
	_, err = editTool.Execute(ctx, `{"path": "sample.go", "TargetContent": "return \"zeta\"", "ReplacementContent": "return \"eta\""}`)
	if err != nil {
		t.Fatalf("edit with TargetContent / ReplacementContent failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"eta\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 7. Top-level single edit with search and replace
	_, err = editTool.Execute(ctx, `{"path": "sample.go", "search": "return \"eta\"", "replace": "return \"theta\""}`)
	if err != nil {
		t.Fatalf("edit with search / replace failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"theta\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 8. updates as a single map/object instead of an array
	_, err = editTool.Execute(ctx, `{"path": "sample.go", "updates": {"oldText": "return \"theta\"", "newText": "return \"iota\""}}`)
	if err != nil {
		t.Fatalf("edit with updates as map failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"iota\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 9. Root array format: [{"path": "...", "oldText": "...", "newText": "..."}]
	_, err = editTool.Execute(ctx, `[{"path": "sample.go", "oldText": "return \"iota\"", "newText": "return \"kappa\""}]`)
	if err != nil {
		t.Fatalf("edit with root array failed: %v", err)
	}
	data, _ = os.ReadFile(testFile)
	if !strings.Contains(string(data), "return \"kappa\"") {
		t.Fatalf("content not updated: %s", string(data))
	}

	// 10. Missing path triggers "missing required argument: path"
	_, err = editTool.Execute(ctx, `{"updates": [{"oldText": "return \"kappa\"", "newText": "return \"lambda\""}]}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: path") {
		t.Fatalf("expected missing required argument: path error, got: %v", err)
	}
}

func TestReadAndWriteToolAliases(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &fileTestContext{root: tmpDir}
	readTool := NewReadTool()
	writeTool := NewWriteTool()

	// 1. writeTool with codeContent and TargetFile
	_, err := writeTool.Execute(ctx, `{"TargetFile": "written.txt", "codeContent": "line 1\nline 2"}`)
	if err != nil {
		t.Fatalf("writeTool with TargetFile and codeContent failed: %v", err)
	}

	// 2. readTool with filePath
	out, err := readTool.Execute(ctx, `{"filePath": "written.txt"}`)
	if err != nil {
		t.Fatalf("readTool with filePath failed: %v", err)
	}
	if !strings.Contains(out, "line 1") || !strings.Contains(out, "line 2") {
		t.Fatalf("readTool unexpected output: %s", out)
	}

	// 3. writeTool nested in input
	_, err = writeTool.Execute(ctx, `{"input": {"path": "nested.txt", "content": "from nested input"}}`)
	if err != nil {
		t.Fatalf("writeTool with nested input failed: %v", err)
	}

	// 4. readTool nested in input
	out, err = readTool.Execute(ctx, `{"input": {"path": "nested.txt"}}`)
	if err != nil {
		t.Fatalf("readTool with nested input failed: %v", err)
	}
	if !strings.Contains(out, "from nested input") {
		t.Fatalf("readTool unexpected output: %s", out)
	}

	// 5. readTool missing path error
	_, err = readTool.Execute(ctx, `{}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: path") {
		t.Fatalf("expected missing path error for readTool, got: %v", err)
	}

	// 6. writeTool missing path error
	_, err = writeTool.Execute(ctx, `{"content": "abc"}`)
	if err == nil || !strings.Contains(err.Error(), "missing required argument: path") {
		t.Fatalf("expected missing path error for writeTool, got: %v", err)
	}
}

func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "sub", "test_atomic.txt")

	data := []byte("hello atomic world")
	if err := filetool.AtomicWriteFile(targetFile, data, 0644); err != nil {
		t.Fatalf("atomicWriteFile failed: %v", err)
	}

	readBack, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed to read back atomically written file: %v", err)
	}
	if string(readBack) != string(data) {
		t.Fatalf("data mismatch: got %q, want %q", string(readBack), string(data))
	}

	// Overwrite atomically
	newData := []byte("overwritten atomically")
	if err := filetool.AtomicWriteFile(targetFile, newData, 0644); err != nil {
		t.Fatalf("atomicWriteFile overwrite failed: %v", err)
	}

	readBack2, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("failed to read back overwritten file: %v", err)
	}
	if string(readBack2) != string(newData) {
		t.Fatalf("data mismatch after overwrite: got %q, want %q", string(readBack2), string(newData))
	}
}
