package agent

import "loop/pkg/db"

// deferredHistorySink stages tool results produced before the provider finishes
// the assistant response. The caller persists the assistant message first and
// then flushes these records with canonical provider call IDs.
type deferredHistorySink struct {
	messages []db.Message
}

func (s *deferredHistorySink) Append(m db.Message)    { s.messages = append(s.messages, m) }
func (s *deferredHistorySink) Messages() []db.Message { return s.messages }
