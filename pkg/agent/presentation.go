package agent

import (
	"io"
	"loop/pkg/ui/style"
	"strings"
)

type StreamRenderer interface {
	Write(content string)
	WriteReasoning(content string)
	Flush()
	HasOutput() bool
	StartToolCall(toolName string, toolCallIndex int)
	SetToolTarget(target string, toolCallIndex int)
	WriteToolCall(content string)
	GetToolTitleLineNumber(index int) int
	DidStreamToolBody(index int) bool
	CompleteToolCall(index int, toolName string, toolArgs string, isError bool)
	GetReasoningDuration() float64
	SetPrompt(prompt string)
}

type TurnLoader interface {
	Pause()
	Resume()
}

type SubagentCancellationDecision uint8

const (
	SubagentCancellationContinue SubagentCancellationDecision = iota
	SubagentCancellationSkipCurrent
	SubagentCancellationStopAll
)

type AgentUI interface {
	DrawStatusBar(w io.Writer, theme style.UITheme)
	DrawPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, spinnerFrame string)
	NewStreamRenderer(w io.Writer, theme style.UITheme, showThinking bool, streamWrites bool, agentName string) StreamRenderer
	SetCollapseStatus(collapsed bool)
	UpdateStatus(model string, promptTokens, completionTokens, currentCompletionTokens int, contextLimit int, isGenerating bool, tps float64, activeTasks int, showTokens bool)
	UpdatePlanStatus(completed, total int)
	DrawStatsLine(w io.Writer, theme style.UITheme, spinnerFrame string, statsText string)
	AskForApproval(w io.Writer, theme style.UITheme) (bool, bool)
	AskUserQuestion(w io.Writer, theme style.UITheme, question string, options []string, recommended string) (string, error)
	AskForSubagentCancellation(w io.Writer, theme style.UITheme, agentName string) SubagentCancellationDecision
	RenderToolHeader(w io.Writer, theme style.UITheme, toolName string, toolArgs string)
	RenderToolOutput(w io.Writer, output string, isError bool, collapseResults bool, theme style.UITheme, toolName string, toolArgs string, bodyWasStreamed bool)
	SetCursorHidden(hidden bool)
	RenderGenerationError(w io.Writer, message string, theme style.UITheme)
}

// Optional renderer capabilities keep terminal-specific replay and preview
// behavior out of the required execution and headless rendering contracts.
type LiveCollapsePresenter interface{ SetLiveCollapse(bool) }
type PendingWritePresenter interface{ MarkPendingWriteResults(string) }
type WriteResultPresenter interface{ TryCompleteWriteResult(int, string) bool }
type ReplaySafety interface{ SafeToReplay() bool }
type TrackedRowInvalidator interface{ InvalidateTrackedRows() }
type ToolResultRecorder interface {
	RecordToolResult(int, string, string, bool)
}
type ProgressCallTracker interface{ TrackProgressCall(int) }

func (a *Agent) ShouldShowThinking() bool {
	if a == nil || a.Config == nil || !a.Config.ShowThinking {
		return false
	}
	effort := strings.ToLower(strings.TrimSpace(a.Config.ReasoningEffort))
	return effort != "off" && effort != "none"
}

func (a *Agent) NewTurnStreamRenderer(w io.Writer, theme style.UITheme, agentName, prompt string) StreamRenderer {
	var sr StreamRenderer
	if a != nil && a.UI != nil {
		streamWrites := a.Config == nil || a.Config.StreamWrites
		sr = a.UI.NewStreamRenderer(w, theme, a.ShouldShowThinking(), streamWrites, agentName)
	} else {
		sr = &FallbackStreamRenderer{w: w}
	}
	sr.SetPrompt(prompt)
	if presenter, ok := sr.(interface{ SetLiveCollapse(bool) }); ok && a != nil && a.Config != nil {
		presenter.SetLiveCollapse(a.Config.CollapseResults)
	}
	if presenter, ok := sr.(interface{ SetInteractiveQuestions(bool) }); ok {
		presenter.SetInteractiveQuestions(a != nil && a.UI != nil && a.Inquirer == nil &&
			(a.Config == nil || a.Config.AskUserMode != "auto_recommended"))
	}
	return sr
}
