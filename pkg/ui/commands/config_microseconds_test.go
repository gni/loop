package commands

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func TestConfigMicrosecondsPersistsAndCanBeDisabled(t *testing.T) {
	directory := t.TempDir()
	a := &agent.Agent{Config: config.DefaultConfig(), ConfigPath: filepath.Join(directory, "config.json"), WorkspaceRoot: directory}
	if a.Config.ShowMicroseconds {
		t.Fatal("microseconds must be disabled by default")
	}
	theme := style.GetTheme("plain")
	messages := []db.Message{{Role: "system", Content: "test"}}
	sessionID := ""
	for _, enabled := range []bool{true, false} {
		var output bytes.Buffer
		handled, quit := HandleSlashCommand(a, fmt.Sprintf("/config set show_microseconds %t", enabled), &messages, nil, &theme, &output, &sessionID, nil, nil, nil)
		if !handled || quit || a.Config.ShowMicroseconds != enabled {
			t.Fatalf("setting microseconds=%t failed: %q", enabled, output.String())
		}
		saved, err := config.LoadConfig(a.ConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		if saved.ShowMicroseconds != enabled {
			t.Fatalf("microseconds=%t did not survive reload", enabled)
		}
		output.Reset()
		render.RenderConfig(&output, saved, theme)
		if !strings.Contains(output.String(), "show microseconds:") {
			t.Fatal("microsecond setting missing from config display")
		}
	}
}
