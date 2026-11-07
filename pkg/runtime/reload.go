package runtime

import (
	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
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
	cfg, err := config.Clone(s.Agent.Config)
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
	plugins := tool.NewToolRegistry()
	if cfg.LocalPlugins {
		dirs := append(tool.PluginSearchDirs(s.Workspace), cfg.PluginDirectories...)
		seen := make(map[string]bool)
		for _, dir := range dirs {
			if seen[dir] || !s.Agent.ProjectResourceAllowed(dir) {
				continue
			}
			seen[dir] = true
			if err := tool.RegisterPluginsContext(s.Agent.Context(), plugins, dir); err != nil {
				return err
			}
		}
	}
	s.mu.Lock()
	s.Agent.ApplyConfig(cfg)
	s.mu.Unlock()
	s.Agent.Registry.ReplacePrefix("plugin__", plugins.GetAllExecutors())
	s.Agent.SetActiveSkills(skills)
	s.mu.Lock()
	s.hooks = loaded.Hooks
	s.loaded = loaded
	s.mu.Unlock()
	s.Agent.SyncSessionSystemPrompt(&s.messages, s.storageID())
	return s.Agent.PersistenceError()
}
