package runtime

import (
	"fmt"
	"loop/pkg/config"
	"loop/pkg/resources"
)

func resourcePaths(configured, previous, next []string) []string {
	removed := make(map[string]bool, len(previous))
	for _, path := range previous {
		removed[path] = true
	}
	seen := make(map[string]bool)
	var result []string
	for _, paths := range [][]string{configured, next} {
		for _, path := range paths {
			if !seen[path] && (!removed[path] || containsPath(next, path)) {
				seen[path] = true
				result = append(result, path)
			}
		}
	}
	return result
}

func containsPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func applyResources(cfg *config.Config, previous, next resources.PackageResources) error {
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]config.ProviderConfig)
	}
	for name := range previous.Providers {
		delete(cfg.Providers, name)
	}
	for name, profile := range next.Providers {
		if _, exists := cfg.Providers[name]; exists {
			return fmt.Errorf("package provider conflicts with configured provider %s", name)
		}
		cfg.Providers[name] = profile
	}
	cfg.SkillDirectories = resourcePaths(cfg.SkillDirectories, previous.Skills, next.Skills)
	cfg.PromptDirectories = resourcePaths(cfg.PromptDirectories, previous.Prompts, next.Prompts)
	cfg.PluginDirectories = resourcePaths(cfg.PluginDirectories, previous.Plugins, next.Plugins)
	cfg.ExtensionDirectories = resourcePaths(cfg.ExtensionDirectories, previous.Extensions, next.Extensions)
	return nil
}
