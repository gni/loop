package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/ui/style"
)

type HistorySink interface {
	Append(m db.Message)
	Messages() []db.Message
}

type agentSink struct {
	a         *Agent
	messages  *[]db.Message
	sessionID string
	label     string
}

func (s *agentSink) Append(m db.Message) {
	*s.messages = append(*s.messages, m)
	if s.sessionID != "" && s.a != nil {
		if err := db.SaveMessage(s.sessionID, m); err != nil {
			s.a.DebugLogError(s.sessionID, "db.SaveMessage ("+s.label+")", err)
		}
	}
}

func (s *agentSink) Messages() []db.Message { return *s.messages }

func NewAgentSink(a *Agent, messages *[]db.Message, sessionID string) HistorySink {
	return &agentSink{a: a, messages: messages, sessionID: sessionID}
}

type EngineRenderer interface {
	ToolHeader(w io.Writer, theme style.UITheme, toolName, args string)
	ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool)
}

type mainRenderer struct{ a *Agent }

func (r *mainRenderer) ToolHeader(w io.Writer, theme style.UITheme, toolName, args string) {
	if r.a != nil && r.a.UI != nil {
		r.a.UI.RenderToolHeader(w, theme, toolName, args)
	} else {
		fmt.Fprintf(w, "tool call: %s %s\n", toolName, args)
	}
}

func (r *mainRenderer) ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool) {
	if r.a != nil && r.a.UI != nil {
		collapse := r.a.Config != nil && r.a.Config.CollapseResults
		r.a.UI.RenderToolOutput(w, output, isError, collapse, theme, toolName, args, bodyStreamed)
	} else if !bodyStreamed {
		fmt.Fprintln(w, output)
	}
}

func NewMainRenderer(a *Agent) EngineRenderer { return &mainRenderer{a: a} }

type EnginePolicy struct {
	AgentName     string
	AllowApproval bool
	RunHooks      bool
	GuidedMode    bool
	SessionKey    string
	DebugPrefix   string
	ShowRecapLine bool
	ExecContext   func() tool.AgentContext
}

type TurnAwareness struct {
	Turn          int
	MaxTurns      int
	RecapInterval int
}

func (ta *TurnAwareness) ShouldRecap() bool {
	return ta != nil && ta.RecapInterval > 0 && ta.Turn > 0 && ta.Turn%ta.RecapInterval == 0
}

func (ta *TurnAwareness) Format(who string, promptTokens, totalCompletion, effectiveLimit int) string {
	pct := 0.0
	if effectiveLimit > 0 {
		pct = float64(promptTokens) / float64(effectiveLimit) * 100.0
	}
	next := ta.RecapInterval - (ta.Turn % ta.RecapInterval)

	return RuntimeMessagef("recap_line", "[recap %s] turn %d/%d | prompt ~%d tokens | completion total ~%d tokens | effective window %d (%.1f%% used) | next recap in %d turn(s). Track your remaining budget before opening new work.",
		who, ta.Turn, ta.MaxTurns, promptTokens, totalCompletion, effectiveLimit, pct, next)
}

type TurnEngine struct {
	agent                      *Agent
	policy                     EnginePolicy
	guard                      *TurnExecutionGuard
	awareness                  *TurnAwareness
	guided                     *GuidedController
	consecutiveGuardRejections int
}

func NewTurnEngine(a *Agent, policy EnginePolicy) *TurnEngine {
	limit := limits.ConsecutiveLimit()
	thresholds := []int{3, 5}
	maxSteps := 30
	interval := 5
	guidedEnabled := policy.GuidedMode
	if a != nil && a.Config != nil {
		if a.Config.RepeatGuardLimit > 0 {
			limit = limits.ConsecutiveLimitClamped(a.Config.RepeatGuardLimit)
		}
		if len(a.Config.RepeatReminderThresholds) > 0 {
			thresholds = a.Config.RepeatReminderThresholds
		}
		if a.Config.MaxReasoningSteps > 0 {
			maxSteps = a.Config.MaxReasoningSteps
		}
		interval = a.Config.RecapInterval
		if a.Config.GuidedMode {
			guidedEnabled = true
		}
	}

	guard := NewTurnExecutionGuardWithConfig(limit, thresholds)
	if a != nil {
		guard.SetObservationInvalidator(a.FileObservations)
		guard.SetPathResolver(func(p string) string {
			if abs, err := a.SafePath(p); err == nil {
				return abs
			}
			return p
		})
	}

	var guidedCtrl *GuidedController
	if a != nil && a.Guided != nil {
		guidedCtrl = a.Guided
		if policy.GuidedMode && !guidedCtrl.IsEnabled() {
			guidedCtrl.SetEnabled(true)
		}
	} else {
		guidedCtrl = NewGuidedController(guidedEnabled)
	}

	return &TurnEngine{
		agent:     a,
		policy:    policy,
		guard:     guard,
		awareness: &TurnAwareness{MaxTurns: maxSteps, RecapInterval: interval},
		guided:    guidedCtrl,
	}
}

func (e *TurnEngine) Guard() *TurnExecutionGuard { return e.guard }
func (e *TurnEngine) Awareness() *TurnAwareness  { return e.awareness }
func (e *TurnEngine) Guided() *GuidedController  { return e.guided }
func (e *TurnEngine) SetGuidedMode(enabled bool) { e.guided.SetEnabled(enabled) }

func (e *TurnEngine) agentLabel() string {
	if e.policy.AgentName != "" {
		return e.policy.AgentName
	}
	return "agent"
}

func (e *TurnEngine) ShouldRecap() bool {
	if e.agent != nil && e.agent.Config != nil && e.agent.Config.DisableRecap {
		return false
	}
	return e.awareness.ShouldRecap()
}

func (e *TurnEngine) BeginTurn() { e.awareness.Turn++ }

func (e *TurnEngine) MaybeRecap(sink HistorySink, promptTokens, totalCompletion, effectiveLimit int, w io.Writer, theme style.UITheme) string {
	if !e.ShouldRecap() {
		return ""
	}
	content := e.awareness.Format(e.agentLabel(), promptTokens, totalCompletion, effectiveLimit)
	sink.Append(db.Message{Role: "user", Content: content})
	if e.policy.ShowRecapLine && e.agent != nil && e.agent.UI != nil {
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(content))
	}
	return content
}

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
	if e.agent.Config != nil && (e.agent.Config.AutoApprove || e.agent.Config.ApprovalAlwaysAnswer) {
		e.guided.ApprovePlan(todos)
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
	return e.policy.AllowApproval && a != nil && a.Config != nil && NeedsApproval(toolName) && !a.Config.AutoApprove && !a.Config.ApprovalAlwaysAnswer
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
	if always {
		e.agent.Config.AutoApprove = true
		if e.agent.ConfigPath != "" {
			_ = config.SaveConfig(e.agent.ConfigPath, e.agent.Config)
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

func (e *TurnEngine) recordRejectedState(output string) {
	if e.agent == nil {
		return
	}
	e.agent.stateMu.Lock()
	e.agent.lastToolOutput = output
	e.agent.lastToolIsError = true
	e.agent.stateMu.Unlock()
}

func (e *TurnEngine) emitRejectedToolCall(
	idx int,
	tc db.ToolCall,
	output string,
	completeHeader bool,
	bodyStreamed bool,
	sink HistorySink,
	sr StreamRenderer,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
) {
	e.recordRejectedState(output)
	if completeHeader {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
	}
	render.ToolOutput(w, theme, output, true, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: output})
}

func (e *TurnEngine) finalizeToolCall(
	idx int,
	tc db.ToolCall,
	rawOutput string,
	toolErr error,
	approvalRendered bool,
	preserveEditOutput bool,
	sink HistorySink,
	sr StreamRenderer,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
) {
	a := e.agent
	e.guard.RecordPostExecution(tc.Function.Name, tc.Function.Arguments, rawOutput, toolErr)
	if reminder := e.guard.GetAdvisoryReminder(tc.Function.Name, tc.Function.Arguments); reminder != "" {
		rawOutput += "\n\n[" + reminder + "]"
	}
	if toolErr != nil {
		rawOutput = FormatToolExecutionFailure(tc.Function.Name, rawOutput, toolErr)
	}
	if rawOutput == "" {
		rawOutput = "(no output)"
	}

	maxOutputBytes := tool.DefaultMaxToolOutputBytes
	if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
		maxOutputBytes = a.Config.MaxToolOutputBytes
	}
	if ceiling := limits.MaxOutputBytes(); ceiling < maxOutputBytes {
		maxOutputBytes = ceiling
	}
	pruned, _, _ := tool.SpillAndPruneOutput(a.WorkspaceRoot, e.policy.SessionKey, tc.ID, tc.Function.Name, rawOutput, maxOutputBytes)

	isSubagentCall := strings.HasPrefix(tc.Function.Name, "subagent__")
	if !isSubagentCall && !approvalRendered {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
	}
	if !isSubagentCall {
		bodyStreamed := sr.DidStreamToolBody(idx) || (preserveEditOutput && a.DidStreamLiveBody())
		render.ToolOutput(w, theme, pruned, toolErr != nil, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
	}

	a.stateMu.Lock()
	if !preserveEditOutput || !IsInspectionTool(tc.Function.Name) || !a.lastToolWasEdit || a.lastToolOutput == "" {
		a.lastToolOutput = pruned
		a.lastToolIsError = toolErr != nil
		a.lastToolWasEdit = tc.Function.Name == "edit"
	}
	a.stateMu.Unlock()

	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: pruned})
	if afterCall != nil {
		afterCall(tc, pruned, toolErr)
	}
}

func (e *TurnEngine) emitHaltNotice(sink HistorySink, w io.Writer, theme style.UITheme) {
	haltNotice := e.HaltNotice()
	sink.Append(db.Message{Role: "assistant", Content: haltNotice})
	if e.agent != nil && e.agent.UI != nil {
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render(haltNotice))
	} else {
		fmt.Fprintln(w, haltNotice)
	}
}

func (e *TurnEngine) checkBeforeHook(tc db.ToolCall) (string, error) {
	if e.policy.RunHooks {
		if allowed, reason := e.agent.runBeforeToolHook(tc); !allowed {
			return RuntimeMessagef("hook_blocked", "Error: Tool execution blocked by before-hook: %s", reason), fmt.Errorf("blocked by hook")
		}
	}
	return "", nil
}

func (e *TurnEngine) applyAfterHook(tc db.ToolCall, output string, err error) (string, error) {
	if e.policy.RunHooks {
		return e.agent.runAfterToolHook(tc, output, err)
	}
	return output, err
}

func (e *TurnEngine) clampParallelToolCalls(assistantMsg *db.Message) {
	if len(assistantMsg.ToolCalls) <= 1 {
		return
	}
	if e.agent.Config != nil && !e.agent.Config.ParallelToolCalls {
		assistantMsg.ToolCalls = assistantMsg.ToolCalls[:1]
		return
	}
	if e.guided.IsEnabled() {
		for _, tc := range assistantMsg.ToolCalls {
			if IsActionTool(tc.Function.Name) {
				assistantMsg.ToolCalls = assistantMsg.ToolCalls[:1]
				return
			}
		}
	}
}

func (e *TurnEngine) ensureToolHeaderRendered(idx int, tc db.ToolCall, assistantContent string, isParallel bool, sr StreamRenderer, w io.Writer, theme style.UITheme, render EngineRenderer) {
	if isParallel {
		if sr.GetToolTitleLineNumber(idx) == -1 {
			e.renderSubagentToolPrefix(w, theme)
		}
		render.ToolHeader(w, theme, tc.Function.Name, tc.Function.Arguments)
		return
	}
	if sr.GetToolTitleLineNumber(idx) == -1 {
		if e.policy.AgentName == "" && idx == 0 && strings.TrimSpace(assistantContent) != "" {
			fmt.Fprintln(w)
		} else {
			e.renderSubagentToolPrefix(w, theme)
		}
		render.ToolHeader(w, theme, tc.Function.Name, tc.Function.Arguments)
	}
}

func (e *TurnEngine) ExecuteToolCalls(
	ctx context.Context,
	assistantMsg *db.Message,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
) (halted bool, aborted bool) {
	a := e.agent
	if a == nil || assistantMsg == nil {
		return false, false
	}

	e.clampParallelToolCalls(assistantMsg)

	if len(assistantMsg.ToolCalls) > 1 {
		return e.executeToolCallsParallel(ctx, assistantMsg, sink, sr, loader, w, theme, render, afterCall)
	}

	for idx, tc := range assistantMsg.ToolCalls {
		if ctx.Err() != nil {
			e.RecordInterruption(sink, assistantMsg.ToolCalls[idx:], "cancelled before execution")
			return true, false
		}
		a.SetLiveBodyStreamed(false)

		e.ensureToolHeaderRendered(idx, tc, assistantMsg.Content, false, sr, w, theme, render)

		if gateErr := e.verifyGuidedGate(sr, loader, tc.Function.Name); gateErr != nil {
			output := gateErr.Error()
			e.emitRejectedToolCall(idx, tc, output, true, sr.DidStreamToolBody(idx), sink, sr, w, theme, render)
			if strings.Contains(output, "aborted by user") {
				return false, true
			}
			continue
		}

		approved, approvalRendered := e.requestApproval(sr, loader, w, theme, idx, tc)
		if !approved {
			output, shouldAbort := e.promptRejectionFeedback(tc.Function.Name)
			e.emitRejectedToolCall(idx, tc, output, !approvalRendered, sr.DidStreamToolBody(idx), sink, sr, w, theme, render)
			if shouldAbort {
				return false, true
			}
			continue
		}

		var toolOutput string
		var toolErr error

		if guardErr := e.checkGuardPreExecution(tc); guardErr != nil {
			toolErr = guardErr
			toolOutput = guardErr.Error()
		} else if hookOut, hookErr := e.checkBeforeHook(tc); hookErr != nil {
			toolOutput, toolErr = hookOut, hookErr
		} else {
			if loader != nil {
				loader.ShowDots()
			}
			toolOutput, toolErr = a.Registry.Execute(executionContext(e, a), tc.Function.Name, tc.Function.Arguments)
			if ctx.Err() != nil {
				e.RecordInterruption(sink, assistantMsg.ToolCalls[idx:], "cancelled during execution")
				return true, false
			}
			toolOutput, toolErr = e.applyAfterHook(tc, toolOutput, toolErr)
		}

		e.finalizeToolCall(idx, tc, toolOutput, toolErr, approvalRendered, true, sink, sr, w, theme, render, afterCall)

		if e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
			e.emitHaltNotice(sink, w, theme)
			return true, false
		}
	}
	return false, false
}

type callPlan struct {
	tc               db.ToolCall
	idx              int
	output           string
	toolErr          error
	skipped          bool
	guardHit         bool
	approvalRendered bool
}

func (e *TurnEngine) executeToolCallsParallel(
	ctx context.Context,
	assistantMsg *db.Message,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
	afterCall func(tc db.ToolCall, output string, toolErr error),
) (halted bool, aborted bool) {
	a := e.agent
	plans := make([]callPlan, len(assistantMsg.ToolCalls))

	for idx, tc := range assistantMsg.ToolCalls {
		plan := &plans[idx]
		plan.tc = tc
		plan.idx = idx

		if ctx.Err() != nil {
			e.RecordInterruption(sink, assistantMsg.ToolCalls[idx:], "cancelled before execution")
			return true, false
		}

		e.ensureToolHeaderRendered(idx, tc, assistantMsg.Content, true, sr, w, theme, render)

		if gateErr := e.verifyGuidedGate(sr, loader, tc.Function.Name); gateErr != nil {
			plan.toolErr = gateErr
			plan.output = gateErr.Error()
			plan.skipped = true
			if strings.Contains(plan.output, "aborted by user") {
				e.emitRejectedToolCall(idx, tc, plan.output, true, false, sink, sr, w, theme, render)
				return false, true
			}
			continue
		}

		approved, approvalRendered := e.requestApproval(sr, loader, w, theme, idx, tc)
		plan.approvalRendered = approvalRendered
		if !approved {
			rejOutput, shouldAbort := e.promptRejectionFeedback(tc.Function.Name)
			if shouldAbort {
				e.emitRejectedToolCall(idx, tc, rejOutput, !approvalRendered, false, sink, sr, w, theme, render)
				return false, true
			}
			plan.toolErr = fmt.Errorf("rejected by user")
			plan.output = rejOutput
			plan.skipped = true
			continue
		}

		if guardErr := e.checkGuardPreExecution(tc); guardErr != nil {
			plan.toolErr = guardErr
			plan.output = guardErr.Error()
			plan.guardHit = true
		}
	}

	workers := limits.ParallelToolWorkers()
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup

	for i := range plans {
		plan := &plans[i]
		if plan.skipped || plan.guardHit {
			continue
		}
		if ctx.Err() != nil {
			e.RecordInterruption(sink, assistantMsg.ToolCalls[i:], "cancelled before execution")
			return true, false
		}

		if hookOut, hookErr := e.checkBeforeHook(plan.tc); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
			continue
		}

		if strings.HasPrefix(plan.tc.Function.Name, "subagent__") {
			output, err := a.Registry.Execute(executionContext(e, a), plan.tc.Function.Name, plan.tc.Function.Arguments)
			plan.output, plan.toolErr = e.applyAfterHook(plan.tc, output, err)
			continue
		}

		wg.Add(1)
		go func(p *callPlan) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}

			a.SetLiveBodyStreamed(false)
			output, err := a.Registry.Execute(executionContext(e, a), p.tc.Function.Name, p.tc.Function.Arguments)
			if ctx.Err() != nil {
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}
			p.output, p.toolErr = e.applyAfterHook(p.tc, output, err)
		}(&plans[i])
	}

	wg.Wait()

	if ctx.Err() != nil {
		e.RecordInterruption(sink, assistantMsg.ToolCalls, "cancelled during execution")
		return true, false
	}

	for i := range plans {
		plan := &plans[i]
		if plan.skipped {
			e.emitRejectedToolCall(i, plan.tc, plan.output, !plan.approvalRendered, false, sink, sr, w, theme, render)
			continue
		}
		e.finalizeToolCall(i, plan.tc, plan.output, plan.toolErr, plan.approvalRendered, false, sink, sr, w, theme, render, afterCall)

		if plan.guardHit && e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
			e.emitHaltNotice(sink, w, theme)
			return true, false
		}
	}

	return false, false
}

func executionContext(e *TurnEngine, a *Agent) tool.AgentContext {
	if e.policy.ExecContext != nil {
		if c := e.policy.ExecContext(); c != nil {
			return c
		}
	}
	return tool.AgentContext(a)
}

func (e *TurnEngine) HaltNotice() string {
	return RuntimeMessagef("halt_notice", "[%s halted: loop protection rejected %d consecutive tool calls. Stop repeating blocked actions and proceed with 'edit'/'write' or provide final response.]", e.agentLabel(), e.guard.maxConsecutiveCalls)
}

func (e *TurnEngine) RecordInterruption(sink HistorySink, calls []db.ToolCall, reason string) {
	for _, tc := range calls {
		sink.Append(db.Message{
			Role:       "tool",
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", reason),
		})
	}
}

type EngineLoader interface {
	Pause()
	Resume()
	ShowDots()
	Feed()
	PauseDots()
}