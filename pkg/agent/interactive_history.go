package agent

import (
	"fmt"

	"loop/pkg/db"
)

// AppendInteractiveMessage records foreground input outside the model loop.
// Even a cancelled save must update the runtime's shutdown recovery snapshot.
func (a *Agent) AppendInteractiveMessage(messages *[]db.Message, id string, msg db.Message) error {
	appendHistoryMessage(a, messages, id, msg)
	if !a.Ephemeral && id != "" {
		if err := a.CheckpointSession(a.Context(), id, *messages); err != nil {
			a.recordPersistenceError(fmt.Errorf("save interactive history: %w", err))
		}
	}
	if a.SyncSessionState != nil {
		if err := a.SyncSessionState(*messages, a.ToolSelection, id, false); err != nil {
			a.recordPersistenceError(fmt.Errorf("sync interactive history: %w", err))
		}
	}
	return a.persistenceError()
}
