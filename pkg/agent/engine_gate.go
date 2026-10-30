package agent

import (
	"fmt"
	"io"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
	"strings"
)

func (e *TurnEngine) verifyGuidedGate(sr StreamRenderer, loader EngineLoader, toolName string) error {
	if e.guided == nil || !e.guided.IsEnabled() || e.agent == nil {
		return nil
	}

	needsPlanApproval, blockErr := e.guided.CheckToolAllowed(e.agent, toolName)
	if blockErr != nil {
		return blockErr
	}
	if !needsPlanApproval {
		return nil
	}

	todos := e.agent.GetTodos()
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
	answer, err := e.agent.AskUser(FormatPlanSummary(todos), options, "Approve plan and execute")
	if err != nil {
		return fmt.Errorf("plan approval cancelled: %w", err)
	}

	trimmed := strings.TrimSpace(answer)
	if strings.EqualFold(trimmed, "Approve plan and execute") || strings.EqualFold(trimmed, "Confirmed") {
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
	resp, err := e.agent.AskUser(
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

func (e *TurnEngine) needsInteractiveApproval(toolName string) bool {
	a := e.agent
	return e.policy.AllowApproval && a != nil && a.Config != nil && NeedsApproval(toolName) && ((e.guided != nil && e.guided.IsEnabled()) || (!a.Config.AutoApprove && !a.Config.ApprovalAlwaysAnswer))
}

func (e *TurnEngine) requestApproval(sr StreamRenderer, loader EngineLoader, w io.Writer, theme style.UITheme, idx int, tc db.ToolCall) (approved bool, approvalRendered bool) {
	if !e.needsInteractiveApproval(tc.Function.Name) {
		return true, false
	}
	if e.agent.UI == nil {
		return false, false
	}

	sr.Flush()
	if loader != nil {
		loader.Pause()
	}
	if !sr.DidStreamToolBody(idx) {
		e.renderPreApprovalDiffPreview(w, theme, tc.Function.Name, tc.Function.Arguments)
	}

	approved, always := e.agent.UI.AskForApproval(GetScreenWriter(w), theme)
	if loader != nil {
		loader.Resume()
	}
	if always && (e.guided == nil || !e.guided.IsEnabled()) {
		e.agent.Config.AutoApprove = true
		if e.agent.ConfigPath != "" {
			if err := config.SaveConfig(e.agent.ConfigPath, e.agent.Config); err != nil {
				e.agent.recordPersistenceError(fmt.Errorf("save automatic approval preference: %w", err))
			}
		}
	}
	return approved, true
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
