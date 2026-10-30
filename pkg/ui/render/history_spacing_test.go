package render

import (
	"bytes"
	"strings"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestHistorySeparatesReplyAndNextPromptWithOneBlankLine(t *testing.T) {
	for _, reply := range []string{"Last reply.", "Last reply.\n\n", "```text\nLast reply.\n```\n"} {
		t.Run(reply, func(t *testing.T) {
			var output bytes.Buffer
			PrintSessionHistory(&output, []db.Message{
				{Role: "user", Content: "first prompt"},
				{Role: "assistant", Content: reply},
				{Role: "user", Content: "next prompt"},
			}, style.GetTheme("plain"), config.DefaultConfig(), false, false, HistoryRenderHooks{})
			shown := style.StripAnsi(output.String())
			if !strings.Contains(shown, "Last reply.\n\n─── prompt ") {
				t.Fatalf("missing blank row before next prompt: %q", shown)
			}
			if strings.Contains(shown, "Last reply.\n\n\n─── prompt ") || strings.HasPrefix(shown, "\n") {
				t.Fatalf("prompt spacing added extra blank rows: %q", shown)
			}
		})
	}
}
