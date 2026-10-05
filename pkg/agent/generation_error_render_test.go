package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type failingProvider struct {
	err      error
	onStream func()
}

func (p *failingProvider) CheckThinkingSupport(context.Context) bool {
	return false
}

func (p *failingProvider) StreamChatCompletions(
	ctx context.Context,
	_ []db.Message,
	_ []tool.Tool,
	_ chan<- StreamChunk,
) (*db.Message, error) {
	if p.onStream != nil {
		p.onStream()
	}
	return nil, p.err
}

type recordingUI struct {
	mockTurnLoaderUI
	renderedErrors []string
}

func (r *recordingUI) NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) StreamRenderer {
	return &fallbackStreamRenderer{w: w}
}

func (r *recordingUI) RenderGenerationError(w io.Writer, message string, theme style.UITheme) {
	r.renderedErrors = append(r.renderedErrors, message)
	fmt.Fprintf(w, "ERROR_BOX: %s\n", message)
}

func TestGenerationErrorRendersDiagnosticInsteadOfOperationCancelled(t *testing.T) {
	expectedErr := errors.New("HTTP request failed: dial tcp 127.0.0.1:8080: connect: connection refused")
	ui := &recordingUI{}
	a := &Agent{
		Config: &config.Config{
			ContextWindowLimit: 128000,
			MaxReasoningSteps:  30,
		},
		LLMProvider: &failingProvider{err: expectedErr},
		Registry:    tool.NewToolRegistry(),
		UI:          ui,
	}

	messages := []db.Message{
		{Role: "system", Content: "system prompt"},
	}

	var output bytes.Buffer
	ctx := context.Background() // NOT cancelled

	a.RunAgentLoop(ctx, &output, &messages, "hi", nil, style.UITheme{}, false, "")

	outStr := output.String()
	if strings.Contains(outStr, "[Operation Cancelled]") {
		t.Fatalf("expected generation error not to print '[Operation Cancelled]', got:\n%s", outStr)
	}

	if len(ui.renderedErrors) != 1 || !strings.Contains(ui.renderedErrors[0], "connection refused") {
		t.Fatalf("expected UI.RenderGenerationError to be called with connection refused, got: %v", ui.renderedErrors)
	}

	if !strings.Contains(outStr, "connection refused") {
		t.Fatalf("expected output to contain error message, got:\n%s", outStr)
	}

	if len(messages) != 3 || messages[2].Role != "error" || !strings.Contains(messages[2].Content, "connection refused") {
		t.Fatalf("expected error message to be appended to history, got: %#v", messages)
	}
}

func TestUserCancellationRendersOperationCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	provider := &failingProvider{
		onStream: func() {
			cancel()
		},
		err: context.Canceled,
	}

	ui := &recordingUI{}
	a := &Agent{
		Config: &config.Config{
			ContextWindowLimit: 128000,
			MaxReasoningSteps:  30,
		},
		LLMProvider: provider,
		Registry:    tool.NewToolRegistry(),
		UI:          ui,
	}

	messages := []db.Message{
		{Role: "system", Content: "system prompt"},
	}

	var output bytes.Buffer
	a.RunAgentLoop(ctx, &output, &messages, "hi", nil, style.UITheme{}, false, "")

	outStr := output.String()
	if !strings.Contains(outStr, "[Operation Cancelled]") {
		t.Fatalf("expected cancelled operation to print '[Operation Cancelled]', got:\n%s", outStr)
	}

	if len(ui.renderedErrors) != 0 {
		t.Fatalf("expected no RenderGenerationError calls on user cancellation, got: %v", ui.renderedErrors)
	}
}
