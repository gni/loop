package runtime

import (
	"fmt"
	"loop/pkg/config"
	"loop/pkg/resources"
	"slices"
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
	return slices.Contains(paths, target)
}

func applyResources(cfg *config.Config, previous, next resources.PackageResources) error {
	if old, wasPackage := previous.Providers[cfg.ActiveProvider]; wasPackage {
		updated, exists := next.Providers[cfg.ActiveProvider]
		if !exists {
			return fmt.Errorf("active package provider %q was removed; select another provider before reloading", cfg.ActiveProvider)
		}
		// Refresh fields changed by the package before ApplyConfig synchronizes
		// the active selection back into its provider profile.
		if old.Endpoint != updated.Endpoint {
			cfg.Endpoint = updated.Endpoint
		}
		if old.APIKey != updated.APIKey {
			cfg.APIKey = updated.APIKey
		}
		if old.Model != updated.Model && updated.Model != "" {
			cfg.Model = updated.Model
		}
		if old.Timeout != updated.Timeout && updated.Timeout > 0 {
			cfg.Timeout = updated.Timeout
		}
	}
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
