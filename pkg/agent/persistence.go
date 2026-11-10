package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

// SaveConfig snapshots settings under their owning lock and permits shutdown to
// interrupt lock acquisition before the configuration transaction starts.
func (a *Agent) SaveConfig() error {
	a.LLMProviderMu.Lock()
	snapshot, err := config.Clone(a.Config)
	path := a.ConfigPath
	a.LLMProviderMu.Unlock()
	if err != nil {
		return err
	}
	return config.SaveConfigContext(a.Context(), path, snapshot)
}

func (a *Agent) SaveMessage(id string, msg db.Message) error {
	return a.SaveMessageContext(a.Context(), id, msg)
}

func (a *Agent) SaveMessageContext(ctx context.Context, id string, msg db.Message) error {
	if id == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.saveMessageContext(ctx, id, msg); err != nil {
		return err
	}
	if msg.HasConversationContent() {
		return a.SavePendingSessionName(ctx, id)
	}
	return nil
}

func (a *Agent) saveMessageContext(ctx context.Context, id string, msg db.Message) error {
	if a.Store != nil {
		if store, ok := a.Store.(session.MessageWriter); ok {
			return store.SaveMessageContext(ctx, id, msg)
		}
		return a.Store.SaveMessage(id, msg)
	}
	store, err := db.DefaultStore()
	if err != nil {
		return err
	}
	return store.SaveMessageContext(ctx, id, msg)
}

func (a *Agent) AppendEntry(id string, record session.Record) error {
	return a.AppendEntryContext(a.Context(), id, record)
}

func (a *Agent) AppendEntryContext(ctx context.Context, id string, record session.Record) error {
	if id == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Store != nil {
		if store, ok := a.Store.(session.ContextEntryAppender); ok {
			return store.AppendEntryContext(ctx, id, record)
		}
		store, ok := a.Store.(session.EntryAppender)
		if !ok {
			return fmt.Errorf("session storage does not support durable context entries")
		}
		return store.AppendEntry(id, record)
	}
	store, err := db.DefaultStore()
	if err != nil {
		return err
	}
	return store.AppendEntryContext(ctx, id, record)
}

func (a *Agent) HasMessages(id string) bool {
	if a.Store != nil {
		return a.Store.HasMessages(id)
	}
	return db.HasMessages(id)
}

func (a *Agent) LoadSessionMessages(id string) ([]db.Message, error) {
	msgs, err := a.loadSessionMessages(a.Context(), id)
	if err != nil {
		return nil, err
	}
	return a.RecoverSessionMessages(id, msgs)
}

func (a *Agent) loadSessionMessages(ctx context.Context, id string) ([]db.Message, error) {
	if id == "" || a.Ephemeral {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var msgs []db.Message
	var err error
	if a.Store != nil {
		if store, ok := a.Store.(session.MessageLoader); ok {
			msgs, err = store.LoadMessagesContext(ctx, id)
		} else {
			msgs, err = a.Store.LoadMessages(id)
		}
	} else {
		var store *db.JSONLStore
		store, err = db.DefaultStore()
		if err == nil {
			msgs, err = store.LoadMessagesContext(ctx, id)
		}
	}
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

func (a *Agent) rewriteSession(ctx context.Context, id string, msgs []db.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Store != nil {
		if store, ok := a.Store.(session.SessionRewriter); ok {
			return store.RewriteSessionContext(ctx, id, msgs)
		}
		return a.Store.RewriteSession(id, msgs)
	}
	store, err := db.DefaultStore()
	if err != nil {
		return err
	}
	return store.RewriteSessionContext(ctx, id, msgs)
}

// CheckpointSession saves an unsaved tail as ordinary messages so display
// history and model context agree. A changed projection requires a context entry.
func (a *Agent) CheckpointSession(ctx context.Context, id string, msgs []db.Message) error {
	if id == "" {
		return nil
	}
	active := false
	for _, msg := range msgs {
		if msg.HasConversationContent() {
			active = true
			break
		}
	}
	if !active {
		return nil
	}
	saved, err := a.loadSessionMessages(ctx, id)
	if err != nil {
		return err
	}
	if len(saved) > len(msgs) {
		return a.rewriteSession(ctx, id, msgs)
	}
	for i, msg := range saved {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Compare persisted fields, allowing omitted empty slices and other
		// in-memory representation details to round-trip through JSON.
		stored, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		current, err := json.Marshal(msgs[i])
		if err != nil {
			return err
		}
		if !bytes.Equal(stored, current) {
			return a.rewriteSession(ctx, id, msgs)
		}
	}
	for _, msg := range msgs[len(saved):] {
		if err := a.SaveMessageContext(ctx, id, msg); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// RecoverSessionMessages repairs a snapshot without loading it from disk again.
func (a *Agent) RecoverSessionMessages(id string, msgs []db.Message) ([]db.Message, error) {
	repaired, changed := message.RecoverToolHistory(msgs)
	if changed {
		if err := a.rewriteSession(a.Context(), id, repaired); err != nil {
			return nil, fmt.Errorf("persist interrupted tool outcomes: %w", err)
		}
	}
	return repaired, nil
}
