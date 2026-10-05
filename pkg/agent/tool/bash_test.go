package tool

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

type bashTestContext struct {
	root string
}

func (c *bashTestContext) SafePath(inputPath string) (string, error) {
	return inputPath, nil
}

func (c *bashTestContext) GetWorkspaceRoot() string {
	return c.root
}

func (c *bashTestContext) GetActiveSkills() []Skill {
	return nil
}

func (c *bashTestContext) ReloadSkills() []Skill {
	return nil
}

func (c *bashTestContext) SpawnTask(string, io.Writer) (string, error) {
	return "", fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) GetTaskStatus(string) (string, string, error) {
	return "", "", fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) KillTask(string) error {
	return fmt.Errorf("background tasks are not supported in this test")
}

func (c *bashTestContext) Context() context.Context {
	return context.Background()
}

func (c *bashTestContext) HasSubagent(string) bool {
	return false
}

func TestBashFailureReturnsCapturedStderr(t *testing.T) {
	executor := NewBashTool()
	ctx := &bashTestContext{root: t.TempDir()}

	output, err := executor.Execute(
		ctx,
		`{"command":"printf 'npm ERR! unable to resolve dependency tree\\n' >&2; exit 17"}`,
	)
	if err == nil {
		t.Fatal("failing command returned no error")
	}
	if !strings.Contains(output, "npm ERR! unable to resolve dependency tree") {
		t.Fatalf("failing command lost stderr: %q", output)
	}
	if !strings.Contains(err.Error(), "exit status 17") {
		t.Fatalf("failing command lost its exit status: %v", err)
	}
}

func TestCleanBackgroundCommand(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{
			input:    "cd /workspace/tests && nohup python main.py > main.log 2>&1 & echo $!",
			expected: "cd /workspace/tests && python main.py > main.log 2>&1",
		},
		{
			input:    "nohup python server.py &",
			expected: "python server.py",
		},
		{
			input:    "go run main.go &",
			expected: "go run main.go",
		},
	}

	for _, tc := range cases {
		got := cleanBackgroundCommand(tc.input)
		if got != tc.expected {
			t.Errorf("cleanBackgroundCommand(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

type backgroundDetectContext struct {
	bashTestContext
	spawnedCmd string
}

func (c *backgroundDetectContext) SpawnTask(cmd string, w io.Writer) (string, error) {
	c.spawnedCmd = cmd
	return "task_1", nil
}

func TestBashAutoDetectsBackgroundShellSyntax(t *testing.T) {
	executor := NewBashTool()
	ctx := &backgroundDetectContext{bashTestContext: bashTestContext{root: t.TempDir()}}

	output, err := executor.Execute(
		ctx,
		`{"command":"cd /workspace/tests && nohup python main.py > main.log 2>&1 & echo $!"}`,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Task spawned in background with ID: task_1") {
		t.Fatalf("expected output to mention task_1, got: %s", output)
	}
	if ctx.spawnedCmd != "cd /workspace/tests && python main.py > main.log 2>&1" {
		t.Fatalf("expected cleaned spawned command, got: %q", ctx.spawnedCmd)
	}
}

func TestBashSpawnedBackgroundChildDoesNotBlock(t *testing.T) {
	executor := NewBashTool()
	ctx := &bashTestContext{root: t.TempDir()}

	start := time.Now()
	output, err := executor.Execute(
		ctx,
		`{"command":"sleep 10 & echo 'fast exit'"}`,
	)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "fast exit") {
		t.Fatalf("expected output 'fast exit', got: %q", output)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("command took %v, expected < 2s (it blocked on background sleep)", elapsed)
	}
}

type cancellableTestContext struct {
	bashTestContext
	ctx context.Context
}

func (c *cancellableTestContext) Context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func TestBashCancellationKillsProcessGroup(t *testing.T) {
	executor := NewBashTool()
	ctxCancel, cancel := context.WithCancel(context.Background())
	testCtx := &cancellableTestContext{
		bashTestContext: bashTestContext{root: t.TempDir()},
		ctx:             ctxCancel,
	}

	start := time.Now()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	output, err := executor.Execute(
		testCtx,
		`{"command":"sleep 30"}`,
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on cancelled command, got nil")
	}
	if !strings.Contains(err.Error(), "command cancelled by user") {
		t.Fatalf("expected 'command cancelled by user', got: %v", err)
	}
	if !strings.Contains(output, "command cancelled by user") {
		t.Fatalf("expected output to mention cancellation, got: %q", output)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v, expected < 2s", elapsed)
	}
}

func TestBashCompoundCommandWithBackgroundServer(t *testing.T) {
	executor := NewBashTool()
	ctx := &bashTestContext{root: t.TempDir()}

	start := time.Now()
	output, err := executor.Execute(
		ctx,
		`{"command":"sleep 10 & sleep 0.1 && echo 'server ready'"}`,
	)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "server ready") {
		t.Fatalf("expected output 'server ready', got: %q", output)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("command took %v, expected < 2s", elapsed)
	}
}
