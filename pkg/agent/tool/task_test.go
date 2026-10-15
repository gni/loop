package tool

import (
	"context"
	"testing"
)

type mockTaskContext struct {
	AgentContext
	status  string
	output  string
	err     error
	command string
	killed  bool
}

func (m *mockTaskContext) Context() context.Context {
	return context.Background()
}

func (m *mockTaskContext) GetTaskStatus(id string) (string, string, error) {
	if m.err != nil {
		return "", "", m.err
	}
	return m.status, m.output, nil
}

func (m *mockTaskContext) GetTaskCommand(id string) string {
	return m.command
}

func (m *mockTaskContext) KillTask(id string) error {
	if m.err != nil {
		return m.err
	}
	m.killed = true
	return nil
}

func (m *mockTaskContext) ResolveTaskID(id string) string {
	if id == "1" {
		return "task_1"
	}
	return id
}

func TestParseTaskID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{input: "task_1", expected: "task_1"},
		{input: "\"task_1\"", expected: "task_1"},
		{input: "1", expected: "1"},
		{input: `{"task_id": "task_1"}`, expected: "task_1"},
		{input: `{"task_id": 1}`, expected: "1"},
		{input: `{"taskId": 2}`, expected: "2"},
		{input: `{"id": 42}`, expected: "42"},
		{input: `{"task": "task_5"}`, expected: "task_5"},
		{input: ``, wantErr: true},
		{input: `{}`, wantErr: true},
	}

	for _, tt := range tests {
		got, err := parseTaskID(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseTaskID(%q) expected error, got %q", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseTaskID(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.expected {
			t.Errorf("parseTaskID(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}

func TestNormalizeName_TaskAndTools(t *testing.T) {
	mappings := map[string]string{
		"task_kill":        "task_kill",
		"kill_task":        "task_kill",
		"stop_task":        "task_kill",
		"task_stop":        "task_kill",
		"taskstop":         "task_kill",
		"taskkill":         "task_kill",
		"terminate_task":   "task_kill",
		"task_status":      "task_status",
		"taskstatus":       "task_status",
		"status_task":      "task_status",
		"task_info":        "task_status",
		"taskinfo":         "task_status",
		"task_view":        "task_status",
		"taskview":         "task_status",
		"check_task":       "task_status",
		"run_command":      "bash",
		"execute_command":  "bash",
		"cmd":              "bash",
		"read_file":        "read",
		"view_file":        "read",
		"write_file":       "write",
		"save_file":        "write",
		"edit_file":        "edit",
		"replace_file_content": "edit",
		"grep_search":      "grep",
		"search_files":     "grep",
		"find_files":       "find",
		"list_dir":         "list",
		"skill":            "load_skill",
		"loadskill":        "load_skill",
		"loadmcp":          "load_mcp",
		"ask":              "ask_user",
		"ask_question":     "ask_user",
	}

	for alias, expected := range mappings {
		normalized := NormalizeName(alias)
		if normalized != expected {
			t.Errorf("NormalizeName(%q) = %q, expected %q", alias, normalized, expected)
		}
	}
}

func TestTaskStatusTool_Execute(t *testing.T) {
	tool := NewTaskStatusTool()
	ctx := &mockTaskContext{
		status:  "running",
		output:  "tick 1\ntick 2",
		command: "for i in 1 2 3 4 5; do echo tick $i; done",
	}

	// Test with numeric task ID JSON object
	res, err := tool.Execute(ctx, `{"task_id": 1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == "" {
		t.Fatal("expected non-empty output")
	}
}

func TestTaskKillTool_Execute(t *testing.T) {
	tool := NewTaskKillTool()
	ctx := &mockTaskContext{}

	res, err := tool.Execute(ctx, `{"task_id": 1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ctx.killed {
		t.Fatal("expected task to be killed")
	}
	if res == "" {
		t.Fatal("expected non-empty result")
	}
}
