package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maquis/pkg/config"
)

func TestLoadSkillsSubdirectorySKILLMD(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "skills_test_")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	subDir := filepath.Join(tmpDir, "loop-brain")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	skillFile := filepath.Join(subDir, "SKILL.md")
	content := `# Loop Brain Skill Guide
This is the loop brain instructions without frontmatter.
`
	if err := os.WriteFile(skillFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write skill file: %v", err)
	}

	skills, err := LoadSkillsFromDirs(tmpDir)
	if err != nil {
		t.Fatalf("LoadSkillsFromDirs failed: %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("expected 1 skill, got %d", len(skills))
	}

	if skills[0].Name != "loop-brain" {
		t.Errorf("expected skill name 'loop-brain', got %q", skills[0].Name)
	}
}

func TestSkillSearchDirsCoversConfiguredAndWorkspacePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}
	dirs := SkillSearchDirs("~/maquis-skills-test", "/workspace")
	if len(dirs) < 3 {
		t.Fatalf("expected configured + workspace dirs, got %v", dirs)
	}
	if dirs[0] != filepath.Join(home, "maquis-skills-test") {
		t.Errorf("expected configured dir first, got %q", dirs[0])
	}
	if !strings.HasSuffix(dirs[1], filepath.Join("workspace", "skills")) {
		t.Errorf("expected workspace skills dir, got %q", dirs[1])
	}

	seen := make(map[string]bool)
	for _, d := range dirs {
		if seen[d] {
			t.Errorf("duplicate search dir %q", d)
		}
		seen[d] = true
		if d != filepath.Join(home, "maquis-skills-test") && filepath.Base(d) != "skills" && filepath.Base(d) != ".agents" {
			t.Errorf("unexpected dir %q", d)
		}
	}
}

func TestReloadSkillsUsesWorkspaceSkillDirs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "skills_reload_")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	skillDir := filepath.Join(tmpDir, "skills")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: workspace-skill\ndescription: loaded from workspace\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(skillDir, "workspace-skill.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	a := &Agent{
		Config:        &config.Config{SkillsDir: filepath.Join(tmpDir, "missing")},
		WorkspaceRoot: tmpDir,
	}
	skills := a.ReloadSkills()
	if len(skills) != 1 {
		t.Fatalf("expected 1 workspace skill, got %d: %+v", len(skills), skills)
	}
	if skills[0].Name != "workspace-skill" {
		t.Errorf("unexpected skill %q", skills[0].Name)
	}
}
