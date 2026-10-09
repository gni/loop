package subagent

import (
	"os"
	"path/filepath"
	"testing"

	"loop/pkg/domain/message"
	domaintool "loop/pkg/domain/tool"
)

func TestValidateAgentName(t *testing.T) {
	valid := []string{"researcher", "code-reviewer", "test_runner_1", "Agent42"}
	for _, name := range valid {
		if err := ValidateAgentName(name); err != nil {
			t.Errorf("expected %q to be valid, got: %v", name, err)
		}
	}

	invalid := []string{"", "-invalid", "_invalid", "has space", "special@char", string(make([]byte, 65))}
	for _, name := range invalid {
		if err := ValidateAgentName(name); err == nil {
			t.Errorf("expected %q to be invalid, got nil", name)
		}
	}
}

func TestValidateAgentLocalSkill(t *testing.T) {
	valid := domaintool.Skill{
		Name:        "reviewer",
		Description: "Reviews code diffs",
		Content:     "Instructions...",
	}
	cleaned, err := ValidateAgentLocalSkill(valid)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if cleaned.Path != "" {
		t.Errorf("expected Path to be cleared, got %q", cleaned.Path)
	}

	invalid := domaintool.Skill{Name: "invalid name!", Description: "test", Content: "test"}
	if _, err := ValidateAgentLocalSkill(invalid); err == nil {
		t.Errorf("expected error for invalid skill name, got nil")
	}
}

func TestStorageSaveLoadDelete(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}

	inherit := true
	def := AgentDef{
		Name:             "analyzer",
		SystemPrompt:     "You analyze data",
		ParentName:       "root",
		SkillNames:       []string{"math"},
		InheritAllSkills: &inherit,
	}

	// 1. Save Definition
	if err := storage.SaveDefinition(def); err != nil {
		t.Fatalf("save definition failed: %v", err)
	}

	// 2. Load Definition
	loaded, err := storage.LoadDefinition("analyzer")
	if err != nil {
		t.Fatalf("load definition failed: %v", err)
	}
	if loaded.Name != def.Name || loaded.SystemPrompt != def.SystemPrompt {
		t.Fatalf("loaded definition mismatch: got %+v, want %+v", loaded, def)
	}

	// 3. List Definitions
	defs, err := storage.ListDefinitions()
	if err != nil {
		t.Fatalf("list definitions failed: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "analyzer" {
		t.Fatalf("unexpected list output: %+v", defs)
	}

	// 4. Save and Load State
	state := AgentState{
		Status: "completed",
		History: []message.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "world"},
		},
	}
	if err := storage.SaveState("analyzer", state); err != nil {
		t.Fatalf("save state failed: %v", err)
	}

	loadedState, err := storage.LoadState("analyzer")
	if err != nil {
		t.Fatalf("load state failed: %v", err)
	}
	if loadedState == nil || loadedState.Status != "completed" || len(loadedState.History) != 2 {
		t.Fatalf("loaded state mismatch: %+v", loadedState)
	}

	// 5. Delete Definition and State
	if err := storage.DeleteDefinition("analyzer"); err != nil {
		t.Fatalf("delete definition failed: %v", err)
	}
	if err := storage.DeleteState("analyzer"); err != nil {
		t.Fatalf("delete state failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, "analyzer.json")); !os.IsNotExist(err) {
		t.Errorf("expected definition file to be deleted")
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "analyzer_state.json")); !os.IsNotExist(err) {
		t.Errorf("expected state file to be deleted")
	}
}

func TestTaskSupervisor(t *testing.T) {
	sup := NewTaskSupervisor()

	sup.RegisterTask("task-1", "worker", "do task")

	task, err := sup.GetTask("task-1")
	if err != nil {
		t.Fatalf("get task failed: %v", err)
	}
	if task.Status != "pending" || task.AgentName != "worker" {
		t.Fatalf("unexpected task state: %+v", task)
	}

	sup.UpdateTaskStatus("task-1", "completed", "result data", nil)
	updated, err := sup.GetTask("task-1")
	if err != nil {
		t.Fatalf("get updated task failed: %v", err)
	}
	if updated.Status != "completed" || updated.Response != "result data" {
		t.Fatalf("unexpected updated task: %+v", updated)
	}

	if err := CheckDepthAllowed(MaxSubagentDepth); err == nil {
		t.Errorf("expected depth error at max depth, got nil")
	}
	if err := CheckDepthAllowed(1); err != nil {
		t.Errorf("unexpected error at depth 1: %v", err)
	}
}
