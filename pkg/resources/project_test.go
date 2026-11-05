package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectProjectContext_Go(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-go-proj-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	goMod := "module example.com/mytest\n\ngo 1.23\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}
	_ = os.Mkdir(filepath.Join(tempDir, "pkg"), 0755)
	_ = os.Mkdir(filepath.Join(tempDir, "cmd"), 0755)

	ctxStr := DetectProjectContext(tempDir)
	if !strings.Contains(ctxStr, "Go (module: example.com/mytest, go: 1.23)") {
		t.Errorf("expected Go module in context, got: %s", ctxStr)
	}
	if !strings.Contains(ctxStr, "Primary Test Command: go test ./...") {
		t.Errorf("expected go test command in context, got: %s", ctxStr)
	}
	if !strings.Contains(ctxStr, "Key Directories: cmd, pkg") {
		t.Errorf("expected Key Directories, got: %s", ctxStr)
	}
}

func TestDetectProjectContext_Node(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-node-proj-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	pkgJSON := `{"name": "my-node-app", "scripts": {"test": "jest", "build": "tsc"}}`
	if err := os.WriteFile(filepath.Join(tempDir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "pnpm-lock.yaml"), []byte("lockfile"), 0644); err != nil {
		t.Fatalf("failed to write pnpm-lock.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "tsconfig.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("failed to write tsconfig.json: %v", err)
	}

	ctxStr := DetectProjectContext(tempDir)
	if !strings.Contains(ctxStr, "TypeScript / Node.js (package: my-node-app, runner: pnpm)") {
		t.Errorf("expected TypeScript / Node.js in context, got: %s", ctxStr)
	}
	if !strings.Contains(ctxStr, "Primary Test Command: pnpm test") {
		t.Errorf("expected pnpm test, got: %s", ctxStr)
	}
	if !strings.Contains(ctxStr, "Primary Build Command: pnpm run build") {
		t.Errorf("expected pnpm run build, got: %s", ctxStr)
	}
}
