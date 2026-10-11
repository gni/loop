package commands

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestHandleToolsCommands(t *testing.T) {
	tmpDir := t.TempDir()
	_ = db.InitDB(tmpDir)

	cfg := config.DefaultConfig()
	a := agent.NewAgent(cfg, tmpDir+"/config.json", nil)
	a.Registry.Register(tool.NewBashTool())
	a.Registry.Register(tool.NewReadTool())
	a.Registry.Register(tool.NewWriteTool())
	a.Registry.Register(tool.NewEditTool())

	var messages []db.Message
	sessionID := db.NewUUID()
	theme := &style.UITheme{}

	// 1. /tools list
	var buf bytes.Buffer
	handled, quit := HandleSlashCommand(a, "/tools list", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /tools list to be handled without quit")
	}
	out := buf.String()
	if !strings.Contains(out, "TOOLS CATALOG") || !strings.Contains(out, "bash") {
		t.Fatalf("expected tools catalog output with bash, got:\n%s", out)
	}

	// 2. /tools disable bash edit
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/tools disable bash edit", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || quit {
		t.Fatalf("expected /tools disable to be handled")
	}
	if !a.IsToolDisabled("bash") || !a.IsToolDisabled("edit") {
		t.Fatalf("expected bash and edit to be disabled")
	}
	if a.IsToolDisabled("read") {
		t.Fatalf("expected read to remain enabled")
	}

	// 3. /tools list after disable
	buf.Reset()
	HandleSlashCommand(a, "/tools list", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !strings.Contains(buf.String(), "disabled") {
		t.Fatalf("expected /tools list to show disabled status")
	}

	// 4. /tools toggle read
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/tools toggle read", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || !a.IsToolDisabled("read") {
		t.Fatalf("expected read to be toggled to disabled")
	}

	// 5. /tools toggle read again
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/tools toggle read", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || a.IsToolDisabled("read") {
		t.Fatalf("expected read to be toggled to enabled")
	}

	// 6. /tools enable bash
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/tools enable bash", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || a.IsToolDisabled("bash") {
		t.Fatalf("expected bash to be enabled")
	}
	if !a.IsToolDisabled("edit") {
		t.Fatalf("expected edit to remain disabled")
	}

	// 7. /tools reset
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/tools reset", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || a.IsToolDisabled("edit") || a.IsToolDisabled("bash") || a.IsToolDisabled("read") {
		t.Fatalf("expected all tools to be enabled after reset")
	}

	// 8. /config set disabled_tools bash,write
	buf.Reset()
	handled, quit = HandleSlashCommand(a, "/config set disabled_tools bash,write", &messages, nil, theme, &buf, &sessionID, nil, nil, nil)
	if !handled || !a.IsToolDisabled("bash") || !a.IsToolDisabled("write") {
		t.Fatalf("expected bash and write to be disabled via /config set")
	}
}
