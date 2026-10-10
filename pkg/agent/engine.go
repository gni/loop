package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/ui/style"
)

// HistorySink abstracts *where* a turn appends messages. The main agent writes into a
// slice + JSONL session store; subagents write into a mutex-guarded history and persist
// agent state. Both now share one tool-execution pipeline (TurnEngine) instead of two
// copies of it (the old pkg/agent/tools.go vs pkg/agent/swarm/loop.go duplication).
type HistorySink interface {
	Append(m db.Message)
	Messages() []db.Message
}

type agentSink struct {
	a         *Agent
	messages  *[]db.Message
	sessionID string
	label     string // debug label for SaveMessage failures, empty = default
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

// NewAgentSink builds the main-loop sink (message slice + JSONL persistence).
func NewAgentSink(a *Agent, messages *[]db.Message, sessionID string) HistorySink {
	return &agentSink{a: a, messages: messages, sessionID: sessionID}
}

// EngineRenderer abstracts the per-loop difference in how tool headers/outputs are
// printed (main agent vs prefixed subagent banner).
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
		collapse := false
		if r.a.Config != nil {
			collapse = r.a.Config.CollapseResults
		}
		r.a.UI.RenderToolOutput(w, output, isError, collapse, theme, toolName, args, bodyStreamed)
	} else if !bodyStreamed {
		fmt.Fprintln(w, output)
	}
}

// NewMainRenderer returns the plain main-agent renderer.
func NewMainRenderer(a *Agent) EngineRenderer { return &mainRenderer{a: a} }

// EnginePolicy makes the previously implicit differences between the two loops explicit:
// subagents historically skipped approval and hooks. That is now a policy, not a fork.
type EnginePolicy struct {
	AgentName     string // "" for the main agent
	AllowApproval bool   // subagents inherit trust at spawn time; main agent asks
	RunHooks      bool   // before/after tool hooks
	SessionKey    string // JSONL session id or "subagent:<name>" Cast" for spill/prune label
	DebugPrefix   string
	ShowRecapLine bool // print the recap line to the terminal in interactive mode
	// ExecContext supplies the AgentContext passed to Registry.Execute. The main loop uses
	// the Agent itself; subagents pass their multiAgentContext so tools observe the correct
	// per-agent context (HasSubagent, Context()). Nil means "use the agent".
	ExecContext func() tool.AgentContext
}

// TurnAwareness is the single place that knows how many turns an agent has executed and
// how much of its context window it has consumed. Every RecapInterval turns it produces
// a recap the agent can read in its own history, so the model is never blind to its turn
// count or token budget when the context window is approached or exceeded.
type TurnAwareness struct {
	Turn          int
	MaxTurns      int
	RecapInterval int
}

func (ta *TurnAwareness) ShouldRecap() bool {
	return ta != nil && ta.RecapInterval > 0 && ta.Turn > 0 && ta.Turn%ta.RecapInterval == 0
}

// Format renders the recap the agent sees in its history.
func (ta *TurnAwareness) Format(who string, promptTokens, totalCompletion, effectiveLimit int) string {
	used := 0
	if effectiveLimit > 0 {
		used = promptTokens
	}
	pct := 0.0
	if effectiveLimit > 0 {
		pct = float64(used) / float64(effectiveLimit) * 100.0
	}
	next := ta.RecapInterval - (ta.Turn % ta.RecapInterval)

	return RuntimeMessagef("recap_line", "[recap %s] turn %d/%d | prompt ~%d tokens | completion total ~%d tokens | effective window %d (%.1f%% used) | next recap in %d turn(s). Track your remaining budget before opening new work.",
		who, ta.Turn, ta.MaxTurns, promptTokens, totalCompletion, effectiveLimit, pct, next)
}

// TurnEngine is the single shared per-turn pipeline: guard wiring, approval (policy),
// hooks (policy), execution, defense formatting, pruning/spill, rendering, history
// append and circuit-breaker halt. Both RunAgentLoop and the subagent loop drive it.
type TurnEngine struct {
	agent     *Agent
	policy    EnginePolicy
	guard     *TurnExecutionGuard
	awareness *TurnAwareness

	consecutiveGuardRejections int
}

func NewTurnEngine(a *Agent, policy EnginePolicy) *TurnEngine {
	limit := limits.ConsecutiveLimit()
	thresholds := []int{3, 5}
	maxSteps := 30
	interval := 5
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

	return &TurnEngine{
		agent:     a,
		policy:    policy,
		guard:     guard,
		awareness: &TurnAwareness{MaxTurns: maxSteps, RecapInterval: interval},
	}
}

func (e *TurnEngine) Guard() *TurnExecutionGuard { return e.guard }
func (e *TurnEngine) Awareness() *TurnAwareness  { return e.awareness }

// ShouldRecap is the canonical gate: it honours the live config flag so
// '/config set disable_recap true' takes effect mid-session without rebuilding
// the engine.
func (e *TurnEngine) ShouldRecap() bool {
	if e.agent != nil && e.agent.Config != nil && e.agent.Config.DisableRecap {
		return false
	}
	return e.awareness.ShouldRecap()
}

// BeginTurn advances the turn counter; called once per reasoning step.
func (e *TurnEngine) BeginTurn() { e.awareness.Turn++ }

// MaybeRecap injects the recap as a user-role message (the only role that is always
// preserved and never orphaned by EnforceToolPairingInvariance). Returns the content
// when a recap was emitted, "" otherwise.
func (e *TurnEngine) MaybeRecap(sink HistorySink, promptTokens, totalCompletion, effectiveLimit int, w io.Writer, theme style.UITheme) string {
	if !e.ShouldRecap() {
		return ""
	}
	who := e.policy.AgentName
	if who == "" {
		who = "agent"
	}
	content := e.awareness.Format(who, promptTokens, totalCompletion, effectiveLimit)
	sink.Append(db.Message{Role: "user", Content: content})
	if e.policy.ShowRecapLine && e.agent != nil && e.agent.UI != nil {
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.Border).Italic(true).Render(content))
	}
	return content
}

// ExecuteToolCalls is the single tool-call pipeline shared by both loops.
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

	if a.Config != nil && !a.Config.ParallelToolCalls && len(assistantMsg.ToolCalls) > 1 {
		assistantMsg.ToolCalls = assistantMsg.ToolCalls[:1]
	}

	for idx, tc := range assistantMsg.ToolCalls {
		if ctx.Err() != nil {
			e.RecordInterruption(sink, assistantMsg.ToolCalls[idx:], "cancelled before execution")
			return true, false
		}
		a.SetLiveBodyStreamed(false)

		isSubagentCall := strings.HasPrefix(tc.Function.Name, "subagent__")

		if sr.GetToolTitleLineNumber(idx) == -1 {
			if e.policy.AgentName == "" && idx == 0 && strings.TrimSpace(assistantMsg.Content) != "" {
				fmt.Fprintln(w)
			} else if e.policy.AgentName != "" {
				prefixStyle := style.NewStyle().Foreground(theme.Highlight).Bold(true)
				fmt.Fprintf(w, "%s [%s] calling tool:\n",
					style.NewStyle().Foreground(theme.Secondary).Bold(true).Render("❖"),
					prefixStyle.Render(e.policy.AgentName))
			}
			render.ToolHeader(w, theme, tc.Function.Name, tc.Function.Arguments)
		}

		approved := false
		approvalRendered := false
		// Pre-answered "always": config opted into unattended loop runs, so the modal is
		// skipped but the decision is recorded as always-approved. Otherwise every action
		// tool prompts, per call (auto_approve defaults to false).
		if e.policy.AllowApproval && a.Config != nil && a.Config.ApprovalAlwaysAnswer && NeedsApproval(tc.Function.Name) {
			approved = true
		} else if e.policy.AllowApproval && a.Config != nil && !a.Config.AutoApprove && NeedsApproval(tc.Function.Name) {
			approvalRendered = true
			sr.Flush()
			if a.UI != nil {
				if loader != nil {
					loader.Pause()
				}

				var always bool
				approved, always = a.UI.AskForApproval(unwrapWriterIfPossible(w), theme)
				if loader != nil {
					loader.Resume()
				}
				if always {
					a.Config.AutoApprove = true
					if a.ConfigPath != "" {
						if err := config.SaveConfig(a.ConfigPath, a.Config); err != nil {
							a.DebugLogError(e.policy.SessionKey, "config.SaveConfig (auto-approve)", err)
						}
					}
				}
			} else {
				approved = true
			}
		} else {
			approved = true
		}

		if !approved {
			output := RuntimeMessage("tool_rejected_by_user", "error: tool execution rejected by user.")
			a.stateMu.Lock()
			a.lastToolOutput = output
			a.lastToolIsError = true
			a.stateMu.Unlock()
			if !approvalRendered {
				sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, true)
			}
			render.ToolOutput(w, theme, output, true, tc.Function.Name, tc.Function.Arguments, sr.DidStreamToolBody(idx))
			sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: output})
			return false, true
		}

		var toolOutput string
		var toolErr error

		if guardErr := e.guard.CheckPreExecution(tc.Function.Name, tc.Function.Arguments); guardErr != nil {
			toolErr = guardErr
			toolOutput = guardErr.Error()
			e.consecutiveGuardRejections++
			if a.DebugLogger != nil {
				a.DebugLogRepetition(e.policy.SessionKey, tc.Function.Name, tc.Function.Arguments, e.guard.ConsecutiveIdenticalCount(), guardErr.Error())
			}
		} else {
			e.consecutiveGuardRejections = 0
			if e.policy.RunHooks {
				allowed, reason := a.runBeforeToolHook(tc)
				if !allowed {
					toolErr = fmt.Errorf("blocked by hook")
					toolOutput = RuntimeMessagef("hook_blocked", "Error: Tool execution blocked by before-hook: %s", reason)
				}
			}
			if toolErr == nil {
				if loader != nil {
					loader.ShowDots()
				}
				execCtx := tool.AgentContext(a)
				if e.policy.ExecContext != nil {
					if c := e.policy.ExecContext(); c != nil {
						execCtx = c
					}
				}
				toolOutput, toolErr = a.Registry.Execute(execCtx, tc.Function.Name, tc.Function.Arguments)
				if ctx.Err() != nil {
					e.RecordInterruption(sink, assistantMsg.ToolCalls[idx:], "cancelled during execution")
					return true, false
				}
				if e.policy.RunHooks {
					toolOutput, toolErr = a.runAfterToolHook(tc, toolOutput, toolErr)
				}
			}
		}

		e.guard.RecordPostExecution(tc.Function.Name, tc.Function.Arguments, toolOutput, toolErr)
		if reminder := e.guard.GetAdvisoryReminder(tc.Function.Name, tc.Function.Arguments); reminder != "" {
			toolOutput += "\n\n[" + reminder + "]"
		}
		if toolErr != nil {
			toolOutput = FormatToolExecutionFailure(tc.Function.Name, toolOutput, toolErr)
		}
		if toolOutput == "" {
			toolOutput = "(no output)"
		}

		maxOutputBytes := tool.DefaultMaxToolOutputBytes
		if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
			maxOutputBytes = a.Config.MaxToolOutputBytes
		}
		pruned, _, _ := tool.SpillAndPruneOutput(a.WorkspaceRoot, e.policy.SessionKey, tc.ID, tc.Function.Name, toolOutput, maxOutputBytes)

		if !isSubagentCall && !approvalRendered {
			sr.CompleteToolCall(idx, tc.Function.Name, tc.Function.Arguments, toolErr != nil)
		}
		if !isSubagentCall {
			bodyStreamed := sr.DidStreamToolBody(idx) || a.DidStreamLiveBody()
			render.ToolOutput(w, theme, pruned, toolErr != nil, tc.Function.Name, tc.Function.Arguments, bodyStreamed)
		}

		a.stateMu.Lock()
		if !IsInspectionTool(tc.Function.Name) || !a.lastToolWasEdit || a.lastToolOutput == "" {
			a.lastToolOutput = pruned
			a.lastToolIsError = toolErr != nil
			a.lastToolWasEdit = tc.Function.Name == "edit"
		}
		a.stateMu.Unlock()

		sink.Append(db.Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: pruned})
		if afterCall != nil {
			afterCall(tc, pruned, toolErr)
		}

		if e.consecutiveGuardRejections >= e.guard.maxConsecutiveCalls {
			haltNotice := e.HaltNotice()
			sink.Append(db.Message{Role: "assistant", Content: haltNotice})
			if a.UI != nil {
				fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render(haltNotice))
			} else {
				fmt.Fprintln(w, haltNotice)
			}
			return true, false
		}
	}
	return false, false
}

func (e *TurnEngine) HaltNotice() string {
	who := e.policy.AgentName
	if who == "" {
		who = "agent"
	}
	return RuntimeMessagef("halt_notice", "[%s halted: loop protection rejected %d consecutive tool calls. Stop repeating blocked actions and proceed with 'edit'/'write' or provide final response.]", who, e.guard.maxConsecutiveCalls)
}

// RecordInterruption keeps tool_call_id pairing for calls that will never execute;
// without it EnforceToolPairingInvariance drops the whole turn. Previously only the
// main loop had this — subagents silently lost turns on cancellation.
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

func unwrapWriterIfPossible(w io.Writer) io.Writer { return unwrapWriter(w) }

// EngineLoader is the spinner contract the engine needs from a turn loader.
type EngineLoader interface {
	Pause()
	Resume()
	ShowDots()
	Feed()
	PauseDots()
}
