package tool

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPromptCatalogOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prompts.json")
	contents := `{
	  "system_prompts": {"default_identity": "Custom identity."},
	  "tool_prompts": {"grep": {"snippet": "Custom snippet"}},
	  "agent_tool_templates": {"subagent_prompt": "Custom agent prompt '%s'."}
	}`
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := LoadPromptCatalog(path); err != nil {
		t.Fatalf("LoadPromptCatalog: %v", err)
	}

	if got := GetToolPrompt("grep").Snippet; got != "Custom snippet" {
		t.Errorf("tool prompt override not applied: %q", got)
	}
	if got := MasterSystemPrompts.DefaultIdentity; got != "Custom identity." {
		t.Errorf("system prompt override not applied: %q", got)
	}
	if got := MasterAgentTemplates.SubagentPrompt; got != "Custom agent prompt '%s'." {
		t.Errorf("agent template override not applied: %q", got)
	}

	// Untouched entries must survive the merge.
	if len(MasterSystemPrompts.CoreRules) == 0 {
		t.Error("core rules were wiped by a partial override file")
	}
	if GetToolPrompt("read").Description == "" {
		t.Error("read tool prompt was wiped by a partial override file")
	}

	// Aliased map mutation: the live map identity must be preserved so aliases
	// in pkg/agent keep working after a reload. A rebound map would not be
	// observable through the previously captured reference.
	live := MasterToolPrompts
	if err := LoadPromptCatalog(path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if live["grep"].Snippet != "Custom snippet" {
		t.Error("MasterToolPrompts was rebound instead of mutated in place")
	}
}

func TestLoadPromptCatalogMissingFileKeepsDefaults(t *testing.T) {
	if err := LoadPromptCatalog(""); err != nil {
		t.Fatalf("LoadPromptCatalog: %v", err)
	}
	if MasterSystemPrompts.DefaultIdentity == "" || len(MasterSystemPrompts.CoreRules) == 0 {
		t.Error("defaults were lost when no catalog file is provided")
	}
}
