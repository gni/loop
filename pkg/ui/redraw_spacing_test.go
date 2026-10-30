package ui

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestFinalRedrawDoesNotAddBlankRowsBelowReply(t *testing.T) {
	for _, content := range []string{"Final reply.", "Final reply.\n\n", "```text\nFinal reply.\n```\n"} {
		t.Run(content, func(t *testing.T) {
			useIsolatedCursorTestUI(t)
			cfg := config.DefaultConfig()
			var output bytes.Buffer
			_, height := GetTerminalSize()
			writer := NewPromptPreservingWriter(&output, height)
			getUI().Config = cfg
			getUI().PPWriter = writer
			a := &agent.Agent{Config: cfg, UI: getUI()}
			messages := []db.Message{
				{Role: "user", Content: strings.Repeat("earlier history\n", height)},
				{Role: "assistant", Content: content},
			}
			reader := &keyInterceptorReader{Agent: a, Messages: &messages}
			RedrawScreen(&output, a, reader, nil)

			// The final visible content occupies the scroll boundary. There
			// must be no additional empty transcript rows above the fixed gap.
			if writer.GetPrintLine() != style.ConsoleScrollBottom(height, 0) || writer.GetPrintCol() != len("Final reply.")+1 {
				t.Fatalf("final redraw left blank transcript rows: row=%d col=%d", writer.GetPrintLine(), writer.GetPrintCol())
			}
			statsRow := height - 4
			if gap := statsRow - writer.GetPrintLine() - 1; gap != style.ActivityGapLines {
				t.Fatalf("gap above stats = %d; want %d", gap, style.ActivityGapLines)
			}
		})
	}
}
