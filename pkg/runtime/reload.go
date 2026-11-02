package runtime

import (
	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/resources"
)

func (s *Session) Reload() error {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	defer s.runMu.Unlock()
	directories := []string{resources.AgentDirectory(s.Agent.ConfigPath)}
	if !s.Agent.Config.DisableProjectPlugins {
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
	trusted := !cfg.DisableProjectPlugins
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
	if !cfg.DisableLocalPlugins {
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
	s.mu.Lock()
	s.view = append(s.view[:0], s.messages...)
	s.mu.Unlock()
	return s.Agent.PersistenceError()
}
