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
	loaded, err := resources.LoadPackages(directories...)
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
	trusted := cfg.ProjectResources
	skillDirs := agent.SkillSearchDirsForWorkspace(cfg.SkillsDir, s.Workspace, trusted)
	for _, extraDir := range cfg.SkillDirectories {
		if s.Agent.ProjectResourceAllowed(extraDir) {
			skillDirs = append(skillDirs, extraDir)
		}
	}
	skills, err := agent.LoadSkillsFromDirs(skillDirs...)
	if err != nil {
		return err
	}
	applyErr := s.Agent.ApplyConfig(cfg)
	if !agent.ConfigApplied(applyErr) {
		return applyErr
	}
	s.Agent.SetActiveSkills(skills)
	s.mu.Lock()
	s.hooks = loaded.Hooks
	s.loaded = loaded
	s.mu.Unlock()
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	return errors.Join(applyErr, s.Agent.PersistenceError())
}
