package ui

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestCollapsedThinkingKeepsOneSummaryAndPreservesReasoning(t *testing.T) {
	useIsolatedCursorTestUI(t)
	var output bytes.Buffer
	renderer := NewStreamRenderer(&output, style.GetTheme("plain"), true, true, "")
	renderer.SetLiveCollapse(true)
	for _, chunk := range []string{"Private reasoning\n", "continues across\n", "several chunks.\n"} {
		renderer.WriteReasoning(chunk)
	}
	if shown := style.StripAnsi(output.String()); shown != "⋮ thinking… · 3 lines hidden\n" {
		t.Fatalf("collapsed reasoning did not stay on one summary row: %q", shown)
	}
	var expanded bytes.Buffer
	renderer.ReplayActiveTurn(&expanded, false, style.GetTheme("plain"))
	if !strings.Contains(style.StripAnsi(expanded.String()), "Private reasoning\ncontinues across\nseveral chunks.") {
		t.Fatalf("collapsing discarded reasoning: %q", expanded.String())
	}
	renderer.EndThinking()
	renderer.Write("Visible answer")
	shown := style.StripAnsi(output.String())
	if strings.Contains(shown, "Private reasoning") || strings.Contains(shown, "thinking…") || strings.Count(shown, "⋮ thought") != 1 {
		t.Fatalf("finished reasoning leaked text or duplicated its summary: %q", shown)
	}
	if !strings.Contains(shown, "3 lines hidden · Ctrl+O to expand\n\nVisible answer") {
		t.Fatalf("thinking summary changed answer spacing: %q", shown)
	}
}

func TestThinkingTogglePreservesPartialLineAndCompletionRow(t *testing.T) {
	useIsolatedCursorTestUI(t)
	cfg := config.DefaultConfig()
	cfg.ShowThinking = true
	cfg.ReasoningEffort = "low"
	cfg.CollapseResults = true
	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 24)
	counter := agent.NewNewlineCounterWriter(writer)
	getUI().Config = cfg
	getUI().PPWriter = writer
	a := &agent.Agent{Config: cfg, ConfigPath: filepath.Join(t.TempDir(), "config.json"), UI: getUI()}
	a.SetCurrentWriter(counter)
	renderer := NewStreamRenderer(counter, style.GetTheme("plain"), true, true, "")
	renderer.SetLiveCollapse(true)
	renderer.WriteReasoning("Private reasoning ")
	a.CurrentStreamReplay = renderer.ReplayActiveTurn
	messages := []db.Message{{Role: "user", Content: "answer the question"}}
	reader := &keyInterceptorReader{Agent: a, Messages: &messages}
	reader.ActiveTurn.Store(true)

	for _, collapsed := range []bool{false, true} {
		cfg.CollapseResults = collapsed
		renderer.SetLiveCollapse(collapsed)
		output.Reset()
		RedrawScreen(&output, a, reader, nil)
		renderer.InvalidateTrackedRows()
		shown := style.StripAnsi(output.String())
		if collapsed {
			if strings.Contains(shown, "Private reasoning") || !strings.Contains(shown, "⋮ thinking…") {
				t.Fatalf("collapse exposed reasoning or lost its indicator: %q", shown)
			}
		} else {
			if !strings.Contains(shown, "Private reasoning ") || writer.GetPrintCol() != len("Private reasoning ")+1 {
				t.Fatalf("expansion lost the partial thinking line: %q", shown)
			}
			renderer.WriteReasoning("continues")
		}
	}
	output.Reset()
	renderer.EndThinking()
	shown := style.StripAnsi(output.String())
	if !strings.Contains(shown, "⋮ thought") || !strings.Contains(output.String(), "\x1b[2K") || strings.Contains(shown, "\n") {
		t.Fatalf("completion did not replace the thinking indicator in place: %q", output.String())
	}
	var expanded bytes.Buffer
	renderer.ReplayActiveTurn(&expanded, false, style.GetTheme("plain"))
	if !strings.Contains(style.StripAnsi(expanded.String()), "Private reasoning continues") {
		t.Fatalf("toggling lost thinking chunks: %q", expanded.String())
	}
}

func TestHistoryCollapsesThinkingWithElapsedTime(t *testing.T) {
	useIsolatedCursorTestUI(t)
	cfg := config.DefaultConfig()
	cfg.ShowThinking = true
	cfg.ReasoningEffort = "low"
	messages := []db.Message{{Role: "assistant", ReasoningContent: "Private reasoning", ReasoningDuration: 1.5, Content: "Visible answer"}}
	for _, collapsed := range []bool{true, false} {
		cfg.CollapseResults = collapsed
		SetCollapseStatus(collapsed)
		var output bytes.Buffer
		PrintSessionHistory(&output, messages, style.GetTheme("plain"), cfg)
		shown := style.StripAnsi(output.String())
		if strings.Contains(shown, "Private reasoning") == collapsed {
			t.Fatalf("collapse=%t did not control reasoning visibility: %q", collapsed, shown)
		}
		if collapsed && !strings.Contains(shown, "1 line hidden") {
			t.Fatalf("history lost its hidden-line count: %q", shown)
		}
		if !strings.Contains(shown, "thought (1s)") || !strings.Contains(shown, "Visible answer") {
			t.Fatalf("history lost the thought duration or answer: %q", shown)
		}
	}
}
