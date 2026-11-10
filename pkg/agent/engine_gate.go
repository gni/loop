package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

var errToolApprovalRejected = errors.New("tool execution rejected by user")

func (e *TurnEngine) verifyGuidedGate(ctx context.Context, sr StreamRenderer, loader EngineLoader, call db.ToolCall) error {
	toolName := call.Function.Name
	if e.guided == nil || !e.guided.IsEnabled() || e.agent == nil || !e.agent.Registry.Policy(toolName, call.Function.Arguments).RequiresPlan {
		return nil
	}

	todos := e.agent.GetTodos()
	if state, ok := executionContext(e, e.agent).(tool.TodoState); ok {
		todos = state.GetTodos()
	}
	needsPlanApproval, blockErr := e.guided.CheckPlanAllowed(todos, toolName)
	if blockErr != nil {
		return blockErr
	}
	if !needsPlanApproval {
		return nil
	}

	sr.Flush()
	if loader != nil {
		loader.Pause()
		defer loader.Resume()
	}

	options := []tool.AskUserOption{
		{Label: "Approve plan and execute"},
		{Label: "Revise plan"},
		{Label: "Abort turn"},
	}
	answer, err := e.agent.askUserInteractively(FormatPlanSummary(todos), options, "Approve plan and execute")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("guided plan requires approval: %w", err)
	}

	trimmed := strings.TrimSpace(answer)
	if strings.EqualFold(trimmed, "Approve plan and execute") {
		e.guided.ApprovePlan(todos)
		return nil
	}
	if strings.EqualFold(trimmed, "Abort turn") || strings.EqualFold(trimmed, "operation cancelled by user") {
		return fmt.Errorf("guided plan aborted by user")
	}

	return fmt.Errorf("user requested plan revision before executing '%s': %s. Update the plan with 'todo' or adjust your approach", toolName, trimmed)
}

func (e *TurnEngine) promptRejectionFeedback(toolName string) (steeringMessage string, abortTurn bool) {
	if e.agent == nil || e.agent.UI == nil || e.guided == nil || !e.guided.IsEnabled() {
		return RuntimeMessage("tool_rejected_by_user", "error: tool execution rejected by user."), true
	}

	options := []tool.AskUserOption{
		{Label: "Skip this tool and continue"},
		{Label: "Abort turn"},
	}
	resp, err := e.agent.askUserInteractively(
		fmt.Sprintf("Tool '%s' was rejected. Select an action or choose Other to provide steering instructions:", toolName),
		options,
		"Skip this tool and continue",
	)
	trimmed := strings.TrimSpace(resp)
	if err != nil || strings.EqualFold(trimmed, "Abort turn") || strings.EqualFold(trimmed, "operation cancelled by user") {
		return RuntimeMessage("tool_rejected_by_user", "error: tool execution rejected by user."), true
	}
	if strings.EqualFold(trimmed, "Skip this tool and continue") || strings.EqualFold(trimmed, "other: wrote nothing") {
		return fmt.Sprintf("User skipped tool '%s'. Proceed with an alternative approach or ask for clarification.", toolName), false
	}
	return fmt.Sprintf("User rejected tool '%s' with steering feedback: %s", toolName, trimmed), false
}

func (e *TurnEngine) renderPreApprovalDiffPreview(w io.Writer, theme style.UITheme, toolName, toolArgs string) {
	if e.agent == nil {
		return
	}
	diffPreview := PreviewToolDiff(e.agent, toolName, toolArgs)
	if strings.TrimSpace(diffPreview) == "" {
		return
	}
	header := style.NewStyle().Foreground(theme.Secondary).Italic(true).Render("  proposed diff preview:")
	fmt.Fprintln(w, header)
	for _, line := range strings.Split(strings.TrimRight(diffPreview, "\r\n"), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
}

func (e *TurnEngine) needsInteractiveApproval(call db.ToolCall) bool {
	a := e.agent
	if !e.policy.AllowApproval || a == nil || e.config == nil || !a.Registry.Policy(call.Function.Name, call.Function.Arguments).RequiresApproval {
		return false
	}
	if e.guided.IsEnabled() {
		return true
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return !e.config.AutoApprove && !a.sessionAutoApprove
}

func (e *TurnEngine) requestApproval(ctx context.Context, sr StreamRenderer, loader EngineLoader, w io.Writer, theme style.UITheme, idx int, tc db.ToolCall) (bool, error) {
	if !e.needsInteractiveApproval(tc) {
		return false, nil
	}
	a := e.agent
	if a.UI == nil {
		return false, fmt.Errorf("tool execution requires interactive approval")
	}
	a.stateMu.Lock()
	generation := a.sessionGeneration
	a.stateMu.Unlock()

	sr.Flush()
	if loader != nil {
		loader.Pause()
		defer loader.Resume()
	}
	if !sr.DidStreamToolBody(idx) {
		e.renderPreApprovalDiffPreview(w, theme, tc.Function.Name, tc.Function.Arguments)
	}

	if err := ctx.Err(); err != nil {
		return false, err
	}
	allowSession := !e.guided.IsEnabled()
	approved, always := a.UI.AskForApproval(GetScreenWriter(w), theme, allowSession)
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if !approved {
		return true, errToolApprovalRejected
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if a.sessionGeneration != generation {
		return true, fmt.Errorf("session changed while waiting for tool approval")
	}
	if always && allowSession {
		a.sessionAutoApprove = true
	}
	return true, nil
}

func (e *TurnEngine) renderSubagentToolPrefix(w io.Writer, theme style.UITheme) {
	if e.policy.AgentName == "" {
		return
	}
	prefixStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
	fmt.Fprintf(w, "%s [%s] calling tool:\n",
		style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("❖"),
		prefixStyle.Render(e.policy.AgentName))
}

func (e *TurnEngine) checkGuardPreExecution(tc db.ToolCall) error {
	guardErr := e.guard.CheckPreExecution(tc.Function.Name, tc.Function.Arguments)
	if guardErr != nil {
		e.consecutiveGuardRejections++
		if e.agent != nil && e.agent.DebugLogger != nil {
			e.agent.DebugLogRepetition(e.policy.SessionKey, tc.Function.Name, tc.Function.Arguments, e.guard.ConsecutiveIdenticalCount(), guardErr.Error())
		}
		return guardErr
	}
	e.consecutiveGuardRejections = 0
	return nil
}
