package runtime

import (
	"errors"

	"loop/pkg/agent"
	"loop/pkg/resources"
)

func (s *Session) Reload() error {
	if err := s.lockIdle(); err != nil {
		return err
	}
	defer s.runMu.Unlock()
	directories := []string{resources.AgentDirectory(s.Agent.ConfigPath)}
	if s.Agent.Config.ProjectResources {
		directories = append(directories, s.Workspace+"/.loop")
	}
	loaded, err := resources.LoadPackagesContext(s.Agent.Context(), directories...)
	if err != nil {
		return err
	}
	cfg, err := s.Configuration()
	if err != nil {
		return err
	}
	if err := applyResources(cfg, s.loaded, loaded); err != nil {
		return err
	}
	applyErr := s.Agent.ReloadConfigResources(cfg)
	if !agent.ConfigApplied(applyErr) {
		return applyErr
	}
	s.mu.Lock()
	s.hooks = loaded.Hooks
	s.loaded = loaded
	s.mu.Unlock()
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	return errors.Join(applyErr, s.Agent.PersistenceError())
}
