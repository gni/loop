package agent

import (
	"bytes"
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
	if s.a != nil {
		s.a.emit(Event{Type: "message_end", SessionID: s.sessionID, Message: &m})
	}
	if s.sessionID != "" && s.a != nil {
		if err := s.a.SaveMessage(s.sessionID, m); err != nil {
			s.a.recordPersistenceError(err)
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
		var rendered bytes.Buffer
		r.a.UI.RenderToolOutput(&rendered, output, isError, collapse, theme, toolName, args, bodyStreamed)
		if rendered.Len() == 0 {
			return
		}
		if counter, ok := w.(*NewlineCounterWriter); ok {
			counter.EnsureNewline()
		}
		_, _ = rendered.WriteTo(w)
	} else if !bodyStreamed {
		fmt.Fprintln(w, output)
	}
}

func NewMainRenderer(a *Agent) EngineRenderer { return &mainRenderer{a: a} }

type EnginePolicy struct {
	AgentName          string
	ToolAllowlist      func() []string
	AllowApproval      bool
	RunHooks           bool
	GuidedMode         bool
	BeforeToolCall     func(index int, call db.ToolCall)
	ToolBatch          *ToolBatch
	SessionKey         string
	DebugPrefix        string
	ShowRecapLine      bool
	InlineEarlyResults bool
	ExecContext        func() tool.AgentContext
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
	limit := a.GetLimits().ConsecutiveLimit
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

func (e *TurnEngine) SetBeforeToolCall(fn func(index int, call db.ToolCall)) {
	e.policy.BeforeToolCall = fn
}

func (e *TurnEngine) SetToolBatch(batch *ToolBatch) {
	e.policy.ToolBatch = batch
}

func (e *TurnEngine) SetInlineEarlyResults(enabled bool) {
	e.policy.InlineEarlyResults = enabled
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
	if e.agent.Config != nil && !e.guided.IsEnabled() && (e.agent.Config.AutoApprove || e.agent.Config.ApprovalAlwaysAnswer) {
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
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.SetResult(idx, output, fmt.Errorf("tool rejected"))
		e.policy.ToolBatch.Finish(idx, true)
	}
	if (tc.Function.Name == "write" || tc.Function.Name == "edit") && bodyStreamed {
		if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
			label := "⎿  Not saved (details below)"
			if tc.Function.Name == "edit" {
				label = "⎿  Not applied (details below)"
			}
			_ = replacer.TryCompleteWriteResult(idx, label)
		}
	}
	if completeHeader {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
	}
	render.ToolOutput(w, theme, output, true, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
	e.agent.emit(Event{Type: "tool_execution_end", SessionID: e.policy.SessionKey, ToolCall: &tc, Output: output, IsError: true})
	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: output, IsError: true})
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
	rich tool.ToolResult,
) {
	a := e.agent
	e.agent.emit(Event{Type: "tool_execution_end", SessionID: e.policy.SessionKey, ToolCall: &tc, Output: rawOutput, IsError: toolErr != nil})
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
	if ceiling := a.GetLimits().MaxOutputBytes; ceiling < maxOutputBytes {
		maxOutputBytes = ceiling
	}
	pruned, _, _ := tool.SpillAndPruneOutput(a.WorkspaceRoot, e.policy.SessionKey, tc.ID, tc.Function.Name, rawOutput, maxOutputBytes)
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.SetResult(idx, pruned, toolErr)
		e.policy.ToolBatch.Finish(idx, toolErr != nil)
	}

	isSubagentCall := strings.HasPrefix(tc.Function.Name, "subagent__")
	if !isSubagentCall && !approvalRendered {
		sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
	}
	if !isSubagentCall {
		livePreviewed := sr.DidStreamToolBody(idx)
		bodyStreamed := livePreviewed || (preserveEditOutput && a.DidStreamLiveBody())
		// The proposed edit was streamed as +/- lines. Render the verified
		// one-line result beside that preview, not a duplicate full diff.
		// On terminal resize, history intentionally shows the authoritative
		// diff instead of the provisional argument preview.
		if tc.Function.Name == "edit" {
			bodyStreamed = false
		}
		resultHandled := false
		if livePreviewed && (tc.Function.Name == "write" || tc.Function.Name == "edit") {
			if toolErr != nil {
				if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
					_ = replacer.TryCompleteWriteResult(idx, "⎿  Error (details below)")
				}
			} else {
				var rendered bytes.Buffer
				render.ToolOutput(&rendered, theme, pruned, false, tc.Function.Name, tc.Function.Arguments, tc.Function.Name == "write")
				if line := verifiedToolSummaryLine(rendered.String()); line != "" {
					if replacer, ok := sr.(interface{ TryCompleteWriteResult(int, string) bool }); ok {
						resultHandled = replacer.TryCompleteWriteResult(idx, line)
					}
					if !resultHandled && e.policy.InlineEarlyResults {
						// Executing beside this exact preview: a terminal without
						// tracked rows can safely append the verified summary here.
						fmt.Fprintln(w, line)
						resultHandled = true
					} else if !e.policy.InlineEarlyResults {
						// Old previews may have scrolled away; never print their
						// result under an unrelated later file.
						resultHandled = true
					}
				}
			}
		}
		if !resultHandled {
			render.ToolOutput(w, theme, pruned, toolErr != nil, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
		}
	}

	a.stateMu.Lock()
	if !preserveEditOutput || !IsInspectionTool(tc.Function.Name) || !a.lastToolWasEdit || a.lastToolOutput == "" {
		a.lastToolOutput = pruned
		a.lastToolIsError = toolErr != nil
		a.lastToolWasEdit = tc.Function.Name == "edit"
	}
	a.stateMu.Unlock()

	sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: pruned, ContentBlocks: rich.Content, Structured: rich.Structured, IsError: toolErr != nil})
	if afterCall != nil {
		afterCall(tc, pruned, toolErr)
	}
}

// verifiedToolSummaryLine extracts the final ⎿ summary from normal tool
// rendering. It must never return an unverified preview or a multiline diff.
func verifiedToolSummaryLine(rendered string) string {
	lines := strings.Split(strings.TrimRight(rendered, "\r\n"), "\n")
	if len(lines) == 0 {
		return ""
	}
	line := lines[len(lines)-1]
	if strings.HasPrefix(strings.TrimSpace(style.StripAnsi(line)), "⎿") {
		return line
	}
	return ""
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

func (e *TurnEngine) shouldRunToolCallsParallel(calls []db.ToolCall) bool {
	// Preview replay runs on the presentation goroutine after the worker pool
	// finishes. It must not disable otherwise safe concurrent tool execution.
	if len(calls) <= 1 || e.agent == nil || e.agent.Config == nil || e.agent.Config.GuidedMode || !e.agent.Config.ParallelToolCalls {
		return false
	}
	// File writes and reads must execute in model order. In particular, a
	// later write must not finish before an earlier write to the same tree.
	for _, tc := range calls {
		if !e.agent.Registry.CanParallel(tc.Function.Name) {
			return false
		}
	}
	if e.guided != nil && e.guided.IsEnabled() {
		for _, tc := range calls {
			if IsActionTool(tc.Function.Name) {
				return false
			}
		}
	}
	return true
}

func (e *TurnEngine) ensureToolHeaderRendered(idx int, tc db.ToolCall, assistantContent string, isParallel bool, sr StreamRenderer, w io.Writer, theme style.UITheme, render EngineRenderer) {
	if e.policy.BeforeToolCall != nil {
		e.policy.BeforeToolCall(idx, tc)
	}
	if isParallel {
		if sr.GetToolTitleLineNumber(idx) == -1 {
			e.renderSubagentToolPrefix(w, theme)
			render.ToolHeader(w, theme, tc.Function.Name, tc.Function.Arguments)
		}
		return
	}
	if sr.GetToolTitleLineNumber(idx) == -1 {
		if e.policy.AgentName == "" && idx == 0 && strings.TrimSpace(assistantContent) != "" {
			// A deferred first tool (especially read) must not add a blank row
			// after any assistant text that was just streamed.
			if counter, ok := w.(*NewlineCounterWriter); ok {
				counter.EnsureNewline()
			} else {
				fmt.Fprint(w, "\n")
			}
		} else {
			e.renderSubagentToolPrefix(w, theme)
		}
		render.ToolHeader(w, theme, tc.Function.Name, tc.Function.Arguments)
	}
}

type callPlan struct {
	tc               db.ToolCall
	executor         tool.ToolExecutor
	idx              int
	output           string
	toolErr          error
	skipped          bool
	guardHit         bool
	approvalRendered bool
	rich             tool.ToolResult
}

func (e *TurnEngine) prepareToolCallPlan(
	ctx context.Context,
	idx int,
	tc db.ToolCall,
	remainingCalls []db.ToolCall,
	assistantContent string,
	isParallel bool,
	deferPresentation bool,
	sink HistorySink,
	sr StreamRenderer,
	loader EngineLoader,
	w io.Writer,
	theme style.UITheme,
	render EngineRenderer,
) (plan callPlan, halted bool, aborted bool) {
	plan = callPlan{tc: tc, idx: idx}
	e.agent.emit(Event{Type: "tool_execution_start", SessionID: e.policy.SessionKey, ToolCall: &tc})
	if ctx.Err() != nil {
		e.RecordInterruption(sink, remainingCalls, "cancelled before execution")
		return plan, true, false
	}
	if idx > 0 && !deferPresentation {
		// A streamed write with a reserved result row needs no extra spacing
		// during execution. Its completion is shown beside its own preview.
		inlineResult := false
		if tc.Function.Name == "write" || tc.Function.Name == "edit" {
			if pending, ok := sr.(interface{ HasPendingWriteResult(int) bool }); ok {
				inlineResult = pending.HasPendingWriteResult(idx)
			}
		}
		if !inlineResult {
			if counter, ok := w.(*NewlineCounterWriter); ok {
				counter.EnsureActivityGap()
			} else {
				fmt.Fprint(w, strings.Repeat("\n", style.ActivityGapLines+1))
			}
		}
	}
	if !isParallel && e.agent != nil {
		e.agent.SetLiveBodyStreamed(false)
	}

	// In a concurrent batch, prepare and execute without printing headings.
	// Render each heading directly before its result, in model order, after
	// the workers complete. This keeps reads, searches, and plugin results
	// paired with the call that produced them.
	if !deferPresentation {
		e.ensureToolHeaderRendered(idx, tc, assistantContent, isParallel, sr, w, theme, render)
	}
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.Start(idx)
	}

	bodyStreamed := !isParallel && sr.DidStreamToolBody(idx)

	if e.policy.ToolAllowlist != nil && !tool.IsAllowedTool(tc.Function.Name, e.policy.ToolAllowlist()) {
		plan.toolErr = fmt.Errorf("tool '%s' is not allowed for this agent", tc.Function.Name)
		plan.output = plan.toolErr.Error()
		plan.skipped = true
		return plan, false, false
	}

	executor, prepared, err := e.agent.Registry.Prepare(tc.Function.Name, tc.Function.Arguments)
	if err != nil {
		plan.toolErr = err
		plan.output = err.Error()
		plan.skipped = true
		return plan, false, false
	}
	tc.Function.Arguments = prepared
	plan.tc = tc
	plan.executor = executor

	if gateErr := e.verifyGuidedGate(sr, loader, tc.Function.Name); gateErr != nil {
		plan.toolErr = gateErr
		plan.output = gateErr.Error()
		plan.skipped = true
		if strings.Contains(plan.output, "aborted by user") {
			e.emitRejectedToolCall(idx, tc, plan.output, true, bodyStreamed, sink, sr, w, theme, render)
			return plan, false, true
		}
		return plan, false, false
	}

	approved, approvalRendered := e.requestApproval(sr, loader, w, theme, idx, tc)
	plan.approvalRendered = approvalRendered
	if !approved {
		rejOutput, shouldAbort := e.promptRejectionFeedback(tc.Function.Name)
		if shouldAbort {
			e.emitRejectedToolCall(idx, tc, rejOutput, !approvalRendered, bodyStreamed, sink, sr, w, theme, render)
			return plan, false, true
		}
		plan.toolErr = fmt.Errorf("rejected by user")
		plan.output = rejOutput
		plan.skipped = true
		return plan, false, false
	}

	if guardErr := e.checkGuardPreExecution(tc); guardErr != nil {
		plan.toolErr = guardErr
		plan.output = guardErr.Error()
		plan.guardHit = true
	}

	return plan, false, false
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
	defer func() {
		if marker, ok := sr.(interface{ MarkPendingWriteResults(string) }); ok {
			marker.MarkPendingWriteResults("Not executed")
		}
	}()

	if e.shouldRunToolCallsParallel(assistantMsg.ToolCalls) {
		return e.executeToolCallsParallel(ctx, assistantMsg, sink, sr, loader, w, theme, render, afterCall)
	}

	for idx, tc := range assistantMsg.ToolCalls {
		halted, aborted := e.ExecuteToolCallAt(ctx, idx, tc, assistantMsg.ToolCalls[idx:], assistantMsg.Content, sink, sr, loader, w, theme, render, afterCall)
		if halted || aborted {
			if aborted {
				e.RecordInterruption(sink, assistantMsg.ToolCalls[idx+1:], "aborted by user")
			}
			return halted, aborted
		}
	}
	return false, false
}

// ExecuteToolCallAt runs one completed call through the same allowlist,
// schema, approval, observation and hook checks as the batch executor. It
// keeps the original batch index for inline completion and lifecycle events.
func (e *TurnEngine) ExecuteToolCallAt(
	ctx context.Context, idx int, tc db.ToolCall, remaining []db.ToolCall,
	assistantContent string, sink HistorySink, sr StreamRenderer,
	loader EngineLoader, w io.Writer, theme style.UITheme,
	render EngineRenderer,
	afterCall func(db.ToolCall, string, error),
) (halted bool, aborted bool) {
	a := e.agent
	plan, halted, aborted := e.prepareToolCallPlan(ctx, idx, tc, remaining, assistantContent, false, false, sink, sr, loader, w, theme, render)
	if halted || aborted {
		return halted, aborted
	}
	tc = plan.tc
	if plan.skipped {
		e.emitRejectedToolCall(idx, tc, plan.output, !plan.approvalRendered, sr.DidStreamToolBody(idx), sink, sr, w, theme, render)
		return false, false
	}
	if !plan.guardHit {
		if hookOut, hookErr := e.checkBeforeHook(tc); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
		} else {
			if loader != nil {
				loader.ShowDots()
			}
			plan.rich, plan.toolErr = tool.ExecutePreparedResult(executionContext(e, a), plan.executor, tc.Function.Arguments)
			plan.output = plan.rich.Output
			if ctx.Err() != nil {
				e.RecordInterruption(sink, remaining, "cancelled during execution")
				return true, false
			}
			plan.output, plan.toolErr = e.applyAfterHook(tc, plan.output, plan.toolErr)
		}
	}
	e.finalizeToolCall(idx, tc, plan.output, plan.toolErr, plan.approvalRendered, true, sink, sr, w, theme, render, afterCall, plan.rich)
	if e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
		e.emitHaltNotice(sink, w, theme)
		return true, false
	}
	return false, false
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
		plan, halted, aborted := e.prepareToolCallPlan(ctx, idx, tc, assistantMsg.ToolCalls[idx:], assistantMsg.Content, true, true, sink, sr, loader, w, theme, render)
		if halted || aborted {
			reason := "cancelled before execution"
			if aborted {
				reason = "aborted by user"
				e.RecordInterruption(sink, assistantMsg.ToolCalls[idx+1:], reason)
			}
			e.RecordInterruption(sink, assistantMsg.ToolCalls[:idx], reason)
			return halted, aborted
		}
		plans[idx] = plan
	}

	workers := a.GetLimits().ParallelToolWorkers
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	// Tool execution remains parallel, but the renderer consumes each result
	// as soon as the next call in model order completes. We never wait for
	// the entire worker pool before presenting the first completed read.
	ready := make(chan int, len(plans))

	for i := range plans {
		plan := &plans[i]
		if plan.skipped || plan.guardHit {
			ready <- i
			continue
		}
		if ctx.Err() != nil {
			plan.toolErr = ctx.Err()
			plan.output = "cancelled before execution"
			ready <- i
			continue
		}

		if hookOut, hookErr := e.checkBeforeHook(plan.tc); hookErr != nil {
			plan.output, plan.toolErr = hookOut, hookErr
			ready <- i
			continue
		}

		if strings.HasPrefix(plan.tc.Function.Name, "subagent__") {
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), plan.executor, plan.tc.Function.Arguments)
			plan.rich = rich
			plan.output, plan.toolErr = e.applyAfterHook(plan.tc, rich.Output, err)
			ready <- i
			continue
		}

		wg.Add(1)
		go func(i int, p *callPlan) {
			defer wg.Done()
			defer func() { ready <- i }()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				p.toolErr = ctx.Err()
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled before execution")
				return
			}

			if ctx.Err() != nil {
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}

			a.SetLiveBodyStreamed(false)
			rich, err := tool.ExecutePreparedResult(executionContext(e, a), p.executor, p.tc.Function.Arguments)
			p.rich = rich
			if ctx.Err() != nil {
				p.toolErr = fmt.Errorf("cancelled during execution")
				p.output = RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", "cancelled during execution")
				return
			}
			p.output, p.toolErr = e.applyAfterHook(p.tc, rich.Output, err)
		}(i, plan)
	}

	go func() {
		wg.Wait()
		close(ready)
	}()

	shouldHalt := false
	completed := make([]bool, len(plans))
	next := 0
	for idx := range ready {
		completed[idx] = true
		for next < len(plans) && completed[next] {
			if ctx.Err() != nil {
				// Drain worker completions before returning; do not render results
				// that would suggest a cancelled tool completed normally.
				break
			}
			plan := &plans[next]
			if next > 0 {
				if counter, ok := w.(*NewlineCounterWriter); ok {
					counter.EnsureActivityGap()
				} else {
					fmt.Fprint(w, strings.Repeat("\n", style.ActivityGapLines+1))
				}
			}
			e.ensureToolHeaderRendered(next, plan.tc, assistantMsg.Content, false, sr, w, theme, render)
			if plan.skipped {
				e.emitRejectedToolCall(next, plan.tc, plan.output, !plan.approvalRendered, false, sink, sr, w, theme, render)
			} else {
				e.finalizeToolCall(next, plan.tc, plan.output, plan.toolErr, plan.approvalRendered, false, sink, sr, w, theme, render, afterCall, plan.rich)
			}
			if plan.guardHit && e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
				shouldHalt = true
			}
			next++
		}
	}
	if ctx.Err() != nil {
		e.RecordInterruption(sink, assistantMsg.ToolCalls[next:], "cancelled during execution")
		return true, false
	}

	if shouldHalt {
		e.emitHaltNotice(sink, w, theme)
		return true, false
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
		if e.policy.ToolBatch != nil {
			e.policy.ToolBatch.CancelByProviderID(tc.ID)
		}
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
