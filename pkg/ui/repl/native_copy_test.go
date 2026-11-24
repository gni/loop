package repl

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func TestWaitNativeCopyExitIgnoresNavigationEscapesAndExitsOnCleanKeys(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes []byte
	}{
		{"enter CR", []byte("\x1b[5~\x1b[6~\x1b[A\x1bOA\r")},
		{"q key", []byte("abc\x1b[B q")},
		{"ctrl-x", []byte{24}},
		{"bare esc", []byte{'\x1b'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan byte, len(tc.bytes))
			for _, b := range tc.bytes {
				ch <- b
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			done := make(chan struct{})
			go func() {
				waitNativeCopyExit(ctx, ch)
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatalf("waitNativeCopyExit timed out on %s", tc.name)
			}
		})
	}
}

func TestRunNativeCopyViewSuspendsMouseDumpsTranscriptAndRestoresState(t *testing.T) {
	cfg := config.DefaultConfig()
	theme := style.GetTheme("studio")
	uiImpl := ui.NewAgentUI(cfg, theme)
	uiImpl.Enabled = true

	var out bytes.Buffer
	stopMouse := terminal.Default().BeginMouseTracking(&out)
	defer stopMouse()
	out.Reset()

	msgs := []db.Message{
		{Role: "user", Content: "Show Euler identity"},
		{Role: "assistant", Content: "Euler identity: e^(i*pi) + 1 = 0"},
	}
	a := &agent.Agent{Config: cfg, UI: uiImpl}
	ki := &interceptor.KeyInterceptorReader{
		InputContext: context.Background(),
		Agent:        a,
		Theme:        theme,
		W:            &out,
		Messages:     &msgs,
		InputChan:    make(chan byte, 16),
	}
	ki.IsAtMainPrompt.Store(true)
	ki.InputChan <- 'q'

	runNativeCopyView(&out, a, ki, nil)

	raw := out.String()
	if !strings.Contains(raw, terminal.DisableMouseTracking) {
		t.Fatalf("expected native copy view to suspend mouse tracking")
	}
	if !strings.Contains(raw, "\x1b[r\x1b[2J\x1b[H") {
		t.Fatalf("expected native copy view to reset scroll margins and clear primary screen")
	}
	if !strings.Contains(raw, "Euler identity") {
		t.Fatalf("expected conversation content in native copy view output")
	}
	if !strings.Contains(raw, "Native copy view (conversation)") {
		t.Fatalf("expected native copy view footer label")
	}
	if !strings.Contains(raw, "\r\n") {
		t.Fatalf("expected CRLF line endings for raw terminal mode")
	}
	if uiImpl.IsInModal() {
		t.Fatalf("expected InInteractiveMenu to be false after exiting native copy view")
	}
}
