package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBashSyntaxColorDefaultAndAliases(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.BashSyntaxColor {
		t.Fatal("expected DefaultConfig().BashSyntaxColor to be true")
	}

	for _, alias := range []string{"bash_syntax_color", "bash_syntax", "bash_color", "bash_colors", "shell_syntax", "shell_color", "shell_colors"} {
		if err := cfg.SetValue(alias, "false"); err != nil {
			t.Fatalf("SetValue(%q, false) failed: %v", alias, err)
		}
		if cfg.BashSyntaxColor {
			t.Fatalf("expected BashSyntaxColor=false after setting %q", alias)
		}
		if err := cfg.SetValue(alias, "true"); err != nil {
			t.Fatalf("SetValue(%q, true) failed: %v", alias, err)
		}
		if !cfg.BashSyntaxColor {
			t.Fatalf("expected BashSyntaxColor=true after setting %q", alias)
		}
	}
}

func TestLoadLegacyConfigDefaultsBashSyntaxColorTrue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"theme":"studio","markdown_render":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigContext(context.Background(), path)
	if err != nil {
		t.Fatalf("LoadConfigContext failed: %v", err)
	}
	if !cfg.BashSyntaxColor {
		t.Fatal("expected legacy config without bash_syntax_color to default to true")
	}
}
