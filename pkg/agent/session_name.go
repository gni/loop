package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"loop/pkg/db"
	"loop/pkg/domain/session"
)

// SessionNameSnapshot includes whether metadata was loaded for the active ID.
func (a *Agent) SessionNameSnapshot() (string, bool) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	return a.sessionName, a.sessionNameKnown
}

// RestoreSessionName installs already loaded metadata without writing it again.
func (a *Agent) RestoreSessionName(id, name string) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if a.activeSessionID == id {
		a.sessionName, a.sessionNameKnown, a.sessionNameDirty = name, true, false
	}
}

// SetSessionName keeps a draft's name in memory until it has saved content.
func (a *Agent) SetSessionName(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name = strings.TrimSpace(name)
	if err := session.ValidateName(name); err != nil {
		return err
	}
	id := a.ActiveSessionID()
	if a.Ephemeral || id == "" {
		return fmt.Errorf("session is ephemeral")
	}
	dirty := true
	if a.HasMessages(id) {
		err := a.saveSessionName(ctx, id, name)
		if err != nil && !errors.Is(err, db.ErrEmptySession) {
			return err
		}
		dirty = err != nil
	}
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	if err := ctx.Err(); err != nil && dirty {
		return err
	}
	if a.activeSessionID != id {
		return fmt.Errorf("active session changed while setting its name")
	}
	a.sessionName, a.sessionNameKnown, a.sessionNameDirty = name, true, dirty
	return nil
}

// SavePendingSessionName is called after content is saved and during final flush.
// A completed save for an old name must not clear a newer pending change.
func (a *Agent) SavePendingSessionName(ctx context.Context, id string) error {
	a.stateMu.Lock()
	name, pending := a.sessionName, a.sessionNameDirty && a.activeSessionID == id
	a.stateMu.Unlock()
	if !pending {
		return nil
	}
	if err := a.saveSessionName(ctx, id, name); err != nil {
		return err
	}
	a.stateMu.Lock()
	if a.activeSessionID == id && a.sessionName == name {
		a.sessionNameDirty = false
	}
	a.stateMu.Unlock()
	return nil
}

func (a *Agent) saveSessionName(ctx context.Context, id, name string) error {
	if a.Store != nil {
		if store, ok := a.Store.(session.NameWriter); ok {
			return store.SetNameContext(ctx, id, name)
		}
		if store, ok := a.Store.(interface{ SetName(string, string) error }); ok {
			if err := ctx.Err(); err != nil {
				return err
			}
			return store.SetName(id, name)
		}
		return fmt.Errorf("session storage does not support names")
	}
	store, err := db.DefaultStore()
	if err != nil {
		return err
	}
	return store.SetNameContext(ctx, id, name)
}
