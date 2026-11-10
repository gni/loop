package runtime

import (
	"context"
	"errors"
	"fmt"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/session"
)

// SelectBranch moves the saved leaf and restores the conversation state at it.
func (s *Session) SelectBranch(ctx context.Context, entryID string) ([]db.Message, error) {
	if err := s.lockIdle(); err != nil {
		return nil, err
	}
	defer s.runMu.Unlock()
	if s.Store == nil || entryID == "" {
		return nil, fmt.Errorf("branch selection requires a saved session and entry ID")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Agent.PersistenceError() != nil {
		if err := s.checkpointMessages(ctx); err != nil {
			return nil, err
		}
	}
	entries, err := s.Store.EntriesContext(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	path := session.LeafPath(entries, entryID)
	if len(path) == 0 {
		return nil, fmt.Errorf("entry %q not found in session %s", entryID, s.ID)
	}
	messages := db.ProjectMessages(path)
	previous, err := s.Configuration()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Clone(previous)
	if err != nil {
		return nil, err
	}
	tools := s.startupTools
	todos, hasSettings, err := restoreSettingsFromPath(path, cfg, &tools, s.options.KeepSelection)
	if err != nil {
		return nil, err
	}
	if !hasSettings {
		tools = s.options.Tools
	}
	if err := s.validateTools(tools); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var applyErr error
	if hasSettings {
		applyErr = s.Agent.ApplyConfig(cfg)
		if !agent.ConfigApplied(applyErr) {
			return nil, applyErr
		}
	}
	// Applying settings can fail before publication. Once applied, restore the
	// previous settings if the store cannot move the leaf.
	if err := s.moveBranch(ctx, entryID); err != nil {
		if hasSettings {
			if restoreErr := s.Agent.ApplyConfig(previous); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore settings after branch failure: %w", restoreErr))
			}
		}
		return nil, err
	}
	if applyErr != nil {
		s.Agent.SetPendingSystemEvent(applyErr.Error())
	}
	s.Agent.ResetSessionApproval()
	_ = s.Agent.SetTodos(todos)
	if err := s.adoptInteractiveContext(ctx, messages, tools, s.ID); err != nil {
		return nil, err
	}
	s.Agent.SyncSessionSystemPrompt(&s.messages, "")
	return s.MessagesSnapshot(), nil
}

func (s *Session) moveBranch(ctx context.Context, entryID string) error {
	if store, ok := s.Store.(interface {
		BranchContext(context.Context, string, string) error
	}); ok {
		return store.BranchContext(ctx, s.ID, entryID)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.Store.Branch(s.ID, entryID)
}
