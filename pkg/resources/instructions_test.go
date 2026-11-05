package resources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionsForDirectories_MultiEcosystem(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "test-instructions-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	cursorRulesContent := "Always run go vet before commit."
	if err := os.WriteFile(filepath.Join(tempDir, ".cursorrules"), []byte(cursorRulesContent), 0644); err != nil {
		t.Fatalf("failed to write .cursorrules: %v", err)
	}

	rulesDir := filepath.Join(tempDir, ".cursor", "rules")
	if err := os.MkdirAll(rulesDir, 0755); err != nil {
		t.Fatalf("failed to mkdir: %v", err)
	}
	extraRuleContent := "Enforce 5-layer locality of behavior."
	if err := os.WriteFile(filepath.Join(rulesDir, "architecture.mdc"), []byte(extraRuleContent), 0644); err != nil {
		t.Fatalf("failed to write mdc rule: %v", err)
	}

	instructions, diagnostics := InstructionsForDirectories(tempDir)
	if len(diagnostics) > 0 {
		t.Errorf("unexpected diagnostics: %v", diagnostics)
	}
	if !strings.Contains(instructions, cursorRulesContent) {
		t.Errorf("expected .cursorrules content in instructions, got:\n%s", instructions)
	}
	if !strings.Contains(instructions, extraRuleContent) {
		t.Errorf("expected architecture.mdc content in instructions, got:\n%s", instructions)
	}
}
