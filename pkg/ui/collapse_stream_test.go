package ui

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	loopruntime "loop/pkg/runtime"
	"loop/pkg/ui/style"
)

type collapseDuringReplyProvider struct {
	calls   atomic.Int32
	release chan struct{}
	phase   string
}

func (*collapseDuringReplyProvider) CheckThinkingSupport(context.Context) bool { return false }

func (p *collapseDuringReplyProvider) StreamChatCompletions(ctx context.Context, _ []db.Message, _ []tool.Tool, chunks chan<- agent.StreamChunk) (*db.Message, error) {
	if p.calls.Add(1) == 1 {
		return &db.Message{Role: "assistant", ToolCalls: []db.ToolCall{{
			ID: "read-before-toggle", Type: "function",
			Function: db.ToolFunction{Name: "read", Arguments: `{"path":"before-toggle.txt"}`},
		}}}, nil
	}
	updates := []agent.StreamChunk{{Type: "text", Content: "Reply is still generating"}}
	if p.phase == "thinking" {
		updates = []agent.StreamChunk{{Type: "reasoning", Content: "Thinking is still streaming"}}
	} else if p.phase == "write" {
		updates = []agent.StreamChunk{
			{Type: "tool_name", ToolCallIndex: 0, Content: "write"},
			{Type: "tool_call", ToolCallIndex: 0, Content: `{"path":"active.txt","content":"active first line\npartial source`},
		}
	}
	for _, update := range updates {
		select {
		case chunks <- update:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case <-p.release:
		return &db.Message{Role: "assistant", Content: "Reply is still generating"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestCtrlOLiveRedrawIncludesRuntimeHistoryBeforeReplyEnds(t *testing.T) {
	for _, phase := range []string{"text", "thinking", "write"} {
		t.Run(phase, func(t *testing.T) { testCtrlOLiveRuntimeHistory(t, phase) })
	}
}

func testCtrlOLiveRuntimeHistory(t *testing.T, phase string) {
	useIsolatedCursorTestUI(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "before-toggle.txt"), []byte("expanded first line\nexpanded second line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.DisableLocalPlugins = true
	cfg.AutoApprove = true
	cfg.ShowThinking = true
	cfg.ReasoningEffort = "low"
	cfg.CollapseResults = false
	session, err := loopruntime.New(loopruntime.Options{
		Config: cfg, ConfigPath: filepath.Join(dir, "config.json"), Workspace: dir,
		NoSession: true, Output: io.Discard, Tools: []string{"read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	a := session.Agent
	a.UI = getUI()
	getUI().Config = a.Config
	provider := &collapseDuringReplyProvider{release: make(chan struct{}), phase: phase}
	activeContent := "Reply is still generating"
	if phase == "thinking" {
		activeContent = "Thinking is still streaming"
	} else if phase == "write" {
		activeContent = "partial source"
	}
	a.LLMProvider = provider
	var release sync.Once
	defer release.Do(func() { close(provider.release) })

	var transcript bytes.Buffer
	messages := []db.Message{{Role: "system", Content: "test instructions"}}
	reader := &keyInterceptorReader{Agent: a, Messages: &messages, AllowedTools: []string{"read"}}
	reader.ActiveTurn.Store(true)
	checked := false
	a.OnStreamProgress = func() bool {
		if checked || provider.calls.Load() < 2 {
			return false
		}
		var active bytes.Buffer
		if a.CurrentStreamReplay == nil {
			return false
		}
		a.CurrentStreamReplay(&active, false, style.GetTheme("plain"))
		if !strings.Contains(style.StripAnsi(active.String()), activeContent) {
			return false
		}
		checked = true
		defer release.Do(func() { close(provider.release) })
		reader.HandleCtrlO()
		if !reader.PendingCollapseRedraw.Swap(false) {
			t.Error("Ctrl+O did not request a live redraw")
		}
		var frame bytes.Buffer
		RedrawScreen(&frame, a, reader, nil)
		shown := style.StripAnsi(frame.String())
		expectedActive := activeContent
		if phase == "thinking" {
			expectedActive = "⋮ thinking…"
			if strings.Contains(shown, activeContent) {
				t.Errorf("collapsed redraw exposed reasoning: %q", shown)
			}
		}
		for _, required := range []string{"current user prompt", "read", "before-toggle.txt", "lines hidden", expectedActive} {
			if !strings.Contains(shown, required) {
				t.Errorf("live redraw omitted %q before the reply finished: %q", required, shown)
			}
		}
		if strings.Contains(shown, "expanded first line") || strings.Contains(shown, "expanded second line") {
			t.Errorf("previous tool body remained expanded during generation: %q", shown)
		}
		if len(messages) != 1 {
			t.Errorf("test did not exercise the stale REPL history: %+v", messages)
		}
		return true
	}
	if _, err := a.RunSession(context.Background(), &transcript, &messages, "current user prompt", []string{"read"}, style.GetTheme("plain"), ""); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("reply completed without exercising the live redraw")
	}
}

func TestCtrlORepeatedLiveWriteToggleRestoresFullBodyAndPartialLine(t *testing.T) {
	useIsolatedCursorTestUI(t)
	cfg := config.DefaultConfig()
	cfg.CollapseResults = false
	var output bytes.Buffer
	writer := NewPromptPreservingWriter(&output, 24)
	getUI().Config = cfg
	getUI().PPWriter = writer
	a := &agent.Agent{Config: cfg, ConfigPath: filepath.Join(t.TempDir(), "config.json"), UI: getUI()}
	renderer := NewStreamRenderer(writer, style.GetTheme("plain"), false, true, "")
	renderer.ShowLiveToolContent(0, "write", "first.txt", "first full line\nsecond full line\n")
	renderer.EndLiveToolContent()
	renderer.RecordToolResult(0, `{"path":"first.txt","content":"first full line\nsecond full line\n"}`, "saved", false)
	renderer.ShowLiveToolContent(1, "write", "second.txt", "second file line\npartial ")
	a.CurrentStreamReplay = renderer.ReplayActiveTurn
	messages := []db.Message{{Role: "user", Content: "write two files"}}
	reader := &keyInterceptorReader{Agent: a, Messages: &messages}
	reader.ActiveTurn.Store(true)

	for _, collapsed := range []bool{true, false, true, false} {
		reader.HandleCtrlO()
		renderer.SetLiveCollapse(cfg.CollapseResults)
		if !reader.PendingCollapseRedraw.Swap(false) {
			t.Fatal("Ctrl+O did not request a live redraw")
		}
		var frame bytes.Buffer
		RedrawScreen(&frame, a, reader, nil)
		renderer.InvalidateTrackedRows()
		shown := strings.ReplaceAll(style.StripAnsi(frame.String()), "\r\n", "\n")
		if cfg.CollapseResults != collapsed {
			t.Fatalf("collapse state = %v; want %v", cfg.CollapseResults, collapsed)
		}
		if collapsed {
			if strings.Contains(shown, "first full line\nsecond full line") || strings.Contains(shown, "second file line\npartial ") {
				t.Fatalf("a previously streamed body remained expanded: %q", shown)
			}
			if writer.GetPrintCol() != 1 {
				t.Fatalf("collapsed redraw left an unfinished source column: %d", writer.GetPrintCol())
			}
		} else {
			for _, body := range []string{"first full line\nsecond full line", "second file line\npartial "} {
				if strings.Count(shown, body) != 1 {
					t.Fatalf("expanded redraw did not restore the entire body once: %q", shown)
				}
			}
			if writer.GetPrintCol() != len("partial ")+1 {
				t.Fatalf("expanded redraw lost the partial source column: %d", writer.GetPrintCol())
			}
		}
	}
	output.Reset()
	renderer.ShowLiveToolContent(1, "write", "second.txt", "continued")
	if shown := style.StripAnsi(output.String()); strings.Contains(shown, "second.txt") || !strings.Contains(shown, "continued") {
		t.Fatalf("continuation repeated the heading or lost source: %q", shown)
	}
}
