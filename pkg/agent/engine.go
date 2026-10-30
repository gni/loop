package agent

import (
	"bytes"
	"fmt"
	"io"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/ui/style"
	"strings"
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

func (e *TurnEngine) recordRejectedState(output string) {
	if e.agent == nil {
		return
	}
	e.agent.stateMu.Lock()
	e.agent.lastToolOutput = output
	e.agent.lastToolIsError = true
	e.agent.stateMu.Unlock()
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
