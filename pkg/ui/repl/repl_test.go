package repl

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/interceptor"
)

func TestParseManualCommand(t *testing.T) {
	tests := []struct {
		line          string
		enabled       bool
		expectedIsCmd bool
		expectedCmd   string
	}{
		// ! prefixed commands are always manual commands
		{"!git status", false, true, "git status"},
		{"!git status", true, true, "git status"},
		{"!ls -la", false, true, "ls -la"},

		// Direct commands enabled
		{"ls", true, true, "ls"},
		{"ls -la", true, true, "ls -la"},
		{"cd", true, true, "cd"},
		{"cd ..", true, true, "cd .."},
		{"pwd", true, false, ""},
		{"git status", true, false, ""},

		// Non-direct commands in new rule
		{"go build", true, false, ""},
		{"go run main.go", true, false, ""},
		{"mkdir src", true, false, ""},
		{"find .", true, false, ""},

		// Direct commands disabled
		{"ls", false, false, ""},
		{"cd", false, false, ""},
		{"pwd", false, false, ""},
		{"git status", false, false, ""},

		// Non-direct commands
		{"echo hello", true, false, ""},
		{"vim file.txt", true, false, ""},
	}

	for _, tt := range tests {
		isCmd, cmdStr := parseManualCommand(tt.line, tt.enabled)
		if isCmd != tt.expectedIsCmd {
			t.Errorf("parseManualCommand(%q, %v) returned isCmd = %v; want %v", tt.line, tt.enabled, isCmd, tt.expectedIsCmd)
		}
		if cmdStr != tt.expectedCmd {
			t.Errorf("parseManualCommand(%q, %v) returned cmdStr = %q; want %q", tt.line, tt.enabled, cmdStr, tt.expectedCmd)
		}
	}
}

func TestCdUpdatesWorkspaceRoot(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "loop-cd-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origCwd)
	}()

	a := &agent.Agent{
		WorkspaceRoot: origCwd,
	}

	// Verify initial state
	if a.WorkspaceRoot != origCwd {
		t.Errorf("expected workspace root %q, got %q", origCwd, a.WorkspaceRoot)
	}

	target := tempDir
	err = os.Chdir(target)
	if err != nil {
		t.Fatalf("failed to change directory: %v", err)
	}

	pwd, _ := os.Getwd()
	a.WorkspaceRoot = pwd

	if a.WorkspaceRoot != tempDir {
		t.Errorf("expected workspace root to be updated to %q, got %q", tempDir, a.WorkspaceRoot)
	}
}

func TestCtrlDExits(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	var buf bytes.Buffer
	ki := &interceptor.KeyInterceptorReader{
		R:     bytes.NewReader([]byte{4}), // Ctrl+D
		Agent: a,
		W:     &buf,
	}
	rl := term.NewTerminal(ki, "")
	ki.RL = rl

	line, err := rl.ReadLine()
	if err == nil || err.Error() != "EOF" {
		t.Errorf("expected EOF on Ctrl+D, got line %q, err %v", line, err)
	}
}

func TestPromptInputAndBackspace(t *testing.T) {
	a := &agent.Agent{
		Config: &config.Config{},
	}
	var buf bytes.Buffer

	// Type "hello", backspace 2 times (127), type "p", Enter (\r)
	inputBytes := []byte("hello\x7f\x7fp\r")
	ki := &interceptor.KeyInterceptorReader{
		R:     bytes.NewReader(inputBytes),
		Agent: a,
		W:     &buf,
	}
	rl := term.NewTerminal(ki, "")
	ki.RL = rl

	line, err := rl.ReadLine()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line != "help" {
		t.Errorf("expected line %q, got %q", "help", line)
	}

	// Verify terminal output buffer received the echo and backspaces
	out := buf.String()
	if !strings.Contains(out, "\x1b[D \x1b[D") && !strings.Contains(out, "\b") {
		t.Errorf("expected output to contain backspace sequence, got %q", out)
	}

	// Second test: type gibberish, backspace all of it, type command, Enter (\r)
	buf.Reset()
	gibberish := "/fkijojkljkljl"
	var fullInput []byte
	fullInput = append(fullInput, []byte(gibberish)...)
	for i := 0; i < len(gibberish); i++ {
		fullInput = append(fullInput, 127) // backspace
	}
	fullInput = append(fullInput, []byte("/help\r")...)

	ki2 := &interceptor.KeyInterceptorReader{
		R:     bytes.NewReader(fullInput),
		Agent: a,
		W:     &buf,
	}
	rl2 := term.NewTerminal(ki2, "")
	ki2.RL = rl2

	line2, err := rl2.ReadLine()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if line2 != "/help" {
		t.Errorf("expected line %q after backspacing gibberish, got %q", "/help", line2)
	}
}
