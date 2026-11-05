package swarm

import (
	"fmt"
	"io"
	"sync"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/ui/style"
)

// historySink adapts MultiAgent to agent.HistorySink so the shared TurnEngine can append
// tool responses without the subagent loop carrying its own copy of the pipeline.
type historySink struct {
	ma  *MultiAgent
	mu  sync.Mutex
	err error
}

func (s *historySink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *historySink) AppendInMemory(m db.Message) {
	if s.ma == nil {
		return
	}
	s.ma.HistoryMu.Lock()
	s.ma.History = append(s.ma.History, m)
	s.ma.HistoryMu.Unlock()
}

func (s *historySink) flushState(status string) {
	if s.ma == nil || s.ma.Manager == nil {
		return
	}
	if err := s.ma.Manager.SaveAgentState(s.ma, status); err != nil {
		s.mu.Lock()
		if s.err == nil {
			s.err = fmt.Errorf("persist subagent %q history: %w", s.ma.Name, err)
		}
		s.mu.Unlock()
	}
}

func (s *historySink) Append(m db.Message) {
	if s.ma == nil {
		return
	}
	s.AppendInMemory(m)
	s.flushState("running")
}

func (s *historySink) Messages() []db.Message {
	if s.ma == nil {
		return nil
	}
	s.ma.HistoryMu.RLock()
	defer s.ma.HistoryMu.RUnlock()
	return message.CloneAll(s.ma.History)
}

// swarmRenderer prefixes output with the agent name, matching the previous subagent look.
type swarmRenderer struct {
	ma *MultiAgent
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

func newSwarmSink(ma *MultiAgent) *historySink { return &historySink{ma: ma} }

func newSwarmRenderer(ma *MultiAgent) agent.EngineRenderer {
	return &swarmRenderer{ma: ma}
}
