package swarm

import (
	"fmt"
	"io"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// historySink adapts MultiAgent to agent.HistorySink so the shared TurnEngine can append
// tool responses without the subagent loop carrying its own copy of the pipeline.
type historySink struct {
	ma *MultiAgent
}

func (s *historySink) Append(m db.Message) {
	if s.ma == nil {
		return
	}
	s.ma.HistoryMu.Lock()
	s.ma.History = append(s.ma.History, m)
	s.ma.HistoryMu.Unlock()
	if s.ma.Manager != nil {
		_ = s.ma.Manager.SaveAgentState(s.ma, "running")
	}
}

func (s *historySink) Messages() []db.Message {
	if s.ma == nil {
		return nil
	}
	s.ma.HistoryMu.RLock()
	defer s.ma.HistoryMu.RUnlock()
	out := make([]db.Message, len(s.ma.History))
	copy(out, s.ma.History)
	return out
}

// swarmRenderer prefixes output with the agent name, matching the previous subagent look.
type swarmRenderer struct {
	ma    *MultiAgent
	theme style.UITheme
}

func (r *swarmRenderer) ToolHeader(w io.Writer, theme style.UITheme, toolName, args string) {
	if r.ma != nil && r.ma.BaseAgent != nil && r.ma.BaseAgent.UI != nil {
		r.ma.BaseAgent.UI.RenderToolHeader(w, theme, toolName, args)
	} else {
		fmt.Fprintf(w, "› %s\n", toolName)
	}
}

func (r *swarmRenderer) ToolOutput(w io.Writer, theme style.UITheme, output string, isError bool, toolName, args string, bodyStreamed bool) {
	if r.ma != nil && r.ma.BaseAgent != nil && r.ma.BaseAgent.UI != nil {
		r.ma.BaseAgent.UI.RenderToolOutput(w, output, isError, r.ma.BaseAgent.Config.CollapseResults, theme, toolName, args, bodyStreamed)
	} else if !bodyStreamed {
		fmt.Fprintln(w, output)
	}
}

func newSwarmSink(ma *MultiAgent) agent.HistorySink { return &historySink{ma: ma} }

func newSwarmRenderer(ma *MultiAgent, theme style.UITheme) agent.EngineRenderer {
	return &swarmRenderer{ma: ma, theme: theme}
}
