package agent

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
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
}

func (s *agentSink) Append(m db.Message) {
	appendHistoryMessage(s.a, s.messages, s.sessionID, m)
	if s.sessionID != "" && s.a != nil {
		if err := s.a.SaveMessage(s.sessionID, m); err != nil {
			s.a.recordPersistenceError(err)
		}
	}
}

func appendHistoryMessage(a *Agent, messages *[]db.Message, id string, m db.Message) {
	if a != nil {
		a.MessagesMu.Lock()
	}
	*messages = append(*messages, m)
	if a != nil {
		a.MessagesMu.Unlock()
		a.emit(Event{Type: "message_end", SessionID: id, Message: &m})
	}
}

func (s *agentSink) Messages() []db.Message {
	if s.a != nil {
		s.a.MessagesMu.RLock()
		defer s.a.MessagesMu.RUnlock()
	}
	return message.CloneAll(*s.messages)
}

func NewAgentSink(a *Agent, messages *[]db.Message, sessionID string) HistorySink {
	return &agentSink{a: a, messages: messages, sessionID: sessionID}
}

type EnginePolicy struct {
	Config         *config.Config
	AgentName      string
	ToolAllowlist  func() []string
	AllowApproval  bool
	RunHooks       bool
	GuidedMode     bool
	BeforeToolCall func(index int, call db.ToolCall)
	ToolBatch      *ToolBatch
	SessionKey     string
	DebugPrefix    string
	ShowRecapLine  bool
	ExecContext    func() tool.AgentContext
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
	if ta.MaxTurns == 0 {
		return fmt.Sprintf("[recap %s] turn %d | nostop | prompt ~%d tokens | completion total ~%d tokens | effective window %d (%.1f%% used)", who, ta.Turn, promptTokens, totalCompletion, effectiveLimit, pct)
	}
	next := ta.RecapInterval - (ta.Turn % ta.RecapInterval)

	return RuntimeMessagef("recap_line", "[recap %s] turn %d/%d | prompt ~%d tokens | completion total ~%d tokens | effective window %d (%.1f%% used) | next recap in %d turn(s). Track your remaining budget before opening new work.",
		who, ta.Turn, ta.MaxTurns, promptTokens, totalCompletion, effectiveLimit, pct, next)
}

type TurnEngine struct {
	agent                      *Agent
	config                     *config.Config
	policy                     EnginePolicy
	guard                      *TurnExecutionGuard
	awareness                  *TurnAwareness
	guided                     *GuidedController
	consecutiveGuardRejections int
	batchFailure               string
}

func NewTurnEngine(a *Agent, policy EnginePolicy) *TurnEngine {
	cfg := policy.Config
	if cfg == nil && a != nil {
		cfg = a.Config
	}
	limit := a.GetLimits().ConsecutiveLimit
	thresholds := []int{3, 5}
	maxSteps := 30
	interval := 5
	guidedEnabled := policy.GuidedMode
	if cfg != nil {
		if cfg.RepeatGuardLimit > 0 {
			limit = limits.ConsecutiveLimitClamped(cfg.RepeatGuardLimit)
		}
		if len(cfg.RepeatReminderThresholds) > 0 {
			thresholds = cfg.RepeatReminderThresholds
		}
		if cfg.MaxReasoningSteps > 0 {
			maxSteps = cfg.MaxReasoningSteps
		}
		if cfg.Nostop.Enabled() && policy.AgentName == "" {
			maxSteps = 0
		}
		interval = cfg.RecapInterval
		if cfg.GuidedMode && policy.AgentName == "" {
			guidedEnabled = true
		}
	}

	guard := NewTurnExecutionGuardWithConfig(limit, thresholds)
	if a != nil {
		guard.SetObservationInvalidator(a.FileObservations)
	}

	var guidedCtrl *GuidedController
	if policy.AgentName == "" && a != nil && a.Guided != nil {
		guidedCtrl = a.Guided
		if policy.GuidedMode && !guidedCtrl.IsEnabled() {
			guidedCtrl.SetEnabled(true)
		}
	} else {
		guidedCtrl = NewGuidedController(guidedEnabled)
	}

	return &TurnEngine{
		agent:     a,
		config:    cfg,
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

func (e *TurnEngine) agentLabel() string {
	if e.policy.AgentName != "" {
		return e.policy.AgentName
	}
	return "agent"
}

func (e *TurnEngine) ShouldRecap() bool {
	if e.config != nil && !e.config.Recap {
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
	sink.Append(db.Message{Role: "user", ContextKind: "runtime", Content: content})
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

func (e *TurnEngine) canRunToolInParallel(tc db.ToolCall) bool {
	if e.agent == nil || e.config == nil || e.agent.Registry == nil || !e.config.ParallelToolCalls || e.config.GuidedMode || e.config.Nostop.Mode == "eco" {
		return false
	}
	policy := e.agent.Registry.Policy(tc.Function.Name, tc.Function.Arguments)
	if !policy.ReadOnly || !policy.Parallel {
		return false
	}
	if e.guided != nil && e.guided.IsEnabled() && policy.RequiresPlan {
		return false
	}
	return true
}

func (e *TurnEngine) ensureToolHeaderRendered(idx int, tc db.ToolCall, assistantContent string, isParallel bool, sr StreamRenderer, w io.Writer, theme style.UITheme, render EngineRenderer) {
	if e.policy.BeforeToolCall != nil {
		e.policy.BeforeToolCall(idx, tc)
	}
	if tc.Function.Name == "task_state" {
		if tracker, ok := sr.(ProgressCallTracker); ok {
			tracker.TrackProgressCall(idx)
		}
		return
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
	return RuntimeMessagef("halt_notice", "[%s halted: loop protection rejected %d consecutive tool calls. Stop repeating blocked actions and proceed or provide a final response.]", e.agentLabel(), e.guard.maxConsecutiveCalls)
}

func (e *TurnEngine) RecordInterruption(sink HistorySink, calls []db.ToolCall, reason string) {
	for _, call := range calls {
		e.recordInterruptedCall(sink, call, reason, nil)
	}
}

func (e *TurnEngine) recordInterruptedCall(sink HistorySink, call db.ToolCall, reason string, observed *evidence.Record) {
	outcome := evidence.NotExecuted
	if strings.Contains(reason, "during execution") {
		outcome = evidence.Unknown
	}
	observed = evidence.Clone(observed)
	if observed == nil {
		observed = &evidence.Record{Kind: "tool", Target: call.Function.Name, Outcome: outcome}
	} else if observed.Outcome == "" {
		observed.Outcome = outcome
	} else {
		outcome = observed.Outcome
	}
	text := RuntimeMessagef("tool_interrupted", "error: tool execution interrupted (%s)", reason)
	if observed.Changed != nil && *observed.Changed {
		text += "\nFile changes were already applied."
	}
	if observed.TaskID != "" {
		text += "\nBackground task started: " + observed.TaskID
	}
	if observed.ExitCode != nil {
		text += fmt.Sprintf("\n[Command exit code: %d]", *observed.ExitCode)
	}
	if e.policy.ToolBatch != nil {
		e.policy.ToolBatch.CancelByProviderID(call.ID)
	}
	if e.agent != nil {
		e.agent.emit(Event{Type: "tool_execution_end", SessionID: e.policy.SessionKey, ToolCall: &call, Output: text, IsError: true})
	}
	sink.Append(db.Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: text, ToolOutcome: outcome,
		Evidence: observed, TaskState: e.observeTaskTool(call, observed, true), IsError: true})
}

type EngineLoader interface {
	Pause()
	Resume()
	ShowDots()
	Feed()
	PauseDots()
}

// HistoryCompacted permits rereading evidence that has left the model context,
// while retaining repeated-failure diagnostics and the operation guard.
func (e *TurnEngine) HistoryCompacted() { e.guard.ForgetInspectionResults() }

func (e *TurnEngine) UserSteered() {
	previous := e.guard
	e.guard = NewTurnExecutionGuardWithConfig(previous.maxConsecutiveCalls, previous.reminderThresholds)
	e.guard.SetObservationInvalidator(previous.invalidator)
	e.consecutiveGuardRejections = 0
}
