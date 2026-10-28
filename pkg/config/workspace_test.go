package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectTrustPrecedenceAndSavingStayScoped(t *testing.T) {
	directory := t.TempDir()
	workspace := filepath.Join(directory, "project")
	if err := os.MkdirAll(filepath.Join(workspace, ".loop"), 0700); err != nil {
		t.Fatal(err)
	}
	userPath := filepath.Join(directory, "user", "config.json")
	user := DefaultConfig()
	user.Model = "user-model"
	user.ActiveProvider = "default"
	if err := SaveConfig(userPath, user); err != nil {
		t.Fatal(err)
	}
	projectPath := filepath.Join(workspace, ".loop", "config.json")
	if err := os.WriteFile(projectPath, []byte(`{"model":"project-model","skills_dir":"skills","max_reasoning_steps":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".loop-trust"), []byte("trusted"), 0600); err != nil {
		t.Fatal(err)
	}
	if WorkspaceTrusted(userPath, workspace) {
		t.Fatal("repository marker granted trust")
	}
	if err := TrustWorkspace(userPath, workspace, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWorkspaceConfig(userPath, workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "project-model" || cfg.MaxReasoningSteps != 7 || cfg.SkillsDir != filepath.Join(workspace, ".loop", "skills") {
		t.Fatalf("project values not resolved: %+v", cfg)
	}
	cfg.MaxReasoningSteps = 9
	before, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(userPath, cfg); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("project settings changed user settings")
	}
	t.Setenv("LOOP_MODEL", "environment-model")
	cfg, err = LoadWorkspaceConfig(userPath, workspace, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "environment-model" || cfg.Sources["model"] != "environment:LOOP_MODEL" {
		t.Fatal("environment precedence lost")
	}
}

func TestSaveDoesNotResolveEnvironmentSecret(t *testing.T) {
	t.Setenv("LOOP_TEST_PRIVATE_KEY", "private-test-value")
	directory := t.TempDir()
	path := filepath.Join(directory, "config.json")
	cfg := DefaultConfig()
	cfg.ActiveProvider = "test"
	cfg.Providers = map[string]ProviderConfig{"test": {Endpoint: "http://localhost:8080", ApiKey: "env:LOOP_TEST_PRIVATE_KEY"}}
	cfg.SyncActiveProvider()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "providers.json"} {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-test-value") {
			t.Fatalf("resolved secret persisted in %s", name)
		}
	}
}
