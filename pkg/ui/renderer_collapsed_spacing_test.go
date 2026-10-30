package ui

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestCollapsedWriteReplacesProgressWithCompletionWithoutBlankRows(t *testing.T) {
	var output bytes.Buffer
	renderer := NewStreamRenderer(&output, style.GetTheme("plain"), false, true, "")
	renderer.SetLiveCollapse(true)
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("word_count_%d.txt", i)
		renderer.ShowLiveToolContent(i, "write", "", "first chunk ")
		renderer.ShowLiveToolContent(i, "write", path, "second chunk\n")
		renderer.ShowLiveToolContent(i, "write", path, "")
		renderer.EndLiveToolContent()
		if !renderer.HasPendingWriteResult(i) {
			t.Fatalf("collapsed write %d did not reuse its progress row for completion: %q", i, output.String())
		}
		if !renderer.TryCompleteWriteResult(i, "⎿  "+path+" (25 bytes)") {
			t.Fatalf("collapsed write %d did not replace its progress row", i)
		}
	}
	shown := style.StripAnsi(output.String())
	if strings.Contains(shown, "receiving write") {
		t.Fatalf("completed writes retained transient progress: %q", shown)
	}
	for i := 0; i < 3; i++ {
		path := fmt.Sprintf("word_count_%d.txt", i)
		if !strings.Contains(shown, "✚ write "+path+"\n⎿  "+path+" (25 bytes)\n") {
			t.Fatalf("write header and summary were separated or duplicated: %q", shown)
		}
	}
	if strings.Contains(shown, "\n\n\n") {
		t.Fatalf("completed collapsed writes have extra blank rows: %q", shown)
	}
}

func TestCollapsedWriteUsesSameProgressRowAfterRedraw(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(fmt.Sprintf("awaiting_result=%t", ended), func(t *testing.T) {
			useIsolatedCursorTestUI(t)
			cfg := config.DefaultConfig()
			cfg.CollapseResults = true
			var output bytes.Buffer
			writer := NewPromptPreservingWriter(&output, 24)
			counter := agent.NewNewlineCounterWriter(writer)
			getUI().Config = cfg
			getUI().PPWriter = writer
			a := &agent.Agent{Config: cfg, ConfigPath: filepath.Join(t.TempDir(), "config.json"), UI: getUI()}
			a.SetCurrentWriter(counter)
			renderer := NewStreamRenderer(counter, style.GetTheme("plain"), false, true, "")
			renderer.SetLiveCollapse(true)
			renderer.ShowLiveToolContent(0, "write", "", "partial source")
			if ended {
				renderer.EndLiveToolContent()
			}
			a.CurrentStreamReplay = renderer.ReplayActiveTurn
			messages := []db.Message{{Role: "user", Content: "write a file"}}
			reader := &keyInterceptorReader{Agent: a, Messages: &messages}
			reader.ActiveTurn.Store(true)
			RedrawScreen(&output, a, reader, nil)
			renderer.InvalidateTrackedRows()
			progressRow := writer.GetPrintLine() - 1
			output.Reset()
			if !ended {
				renderer.ShowLiveToolContent(0, "write", "word_count.go", " continued")
				if !strings.Contains(output.String(), fmt.Sprintf("\x1b[%d;1H\x1b[2K", progressRow-1)) {
					t.Fatalf("late path did not update the existing heading: %q", output.String())
				}
				renderer.EndLiveToolContent()
			}
			if !renderer.HasPendingWriteResult(0) || !renderer.TryCompleteWriteResult(0, "⎿  word_count.go (24 bytes)") {
				t.Fatalf("redraw lost the compact result row: %q", output.String())
			}
			if !strings.Contains(output.String(), fmt.Sprintf("\x1b[%d;1H\x1b[2K", progressRow)) {
				t.Fatalf("completion did not replace the existing progress row: %q", output.String())
			}
			if strings.Contains(output.String(), "\n") {
				t.Fatalf("updating a compact call appended extra rows: %q", output.String())
			}

			// New calls after a redraw must also retain updatable headings.
			renderer.ShowLiveToolContent(1, "write", "", "next source")
			output.Reset()
			renderer.ShowLiveToolContent(1, "write", "word_count.php", " continued")
			if !strings.Contains(output.String(), "word_count.php") || !strings.Contains(output.String(), "\x1b[2K") || strings.Contains(output.String(), "\n") {
				t.Fatalf("new call after redraw lost its heading row: %q", output.String())
			}
		})
	}
}
