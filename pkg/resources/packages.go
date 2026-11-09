package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loop/pkg/config"
	"loop/pkg/packages"
	"loop/pkg/process"
)

type Hook struct {
	Event     string
	Path      string
	Directory string
}

type PackageResources struct {
	Providers  map[string]config.ProviderConfig
	Skills     []string
	Prompts    []string
	Plugins    []string
	Extensions []string
	Hooks      []Hook
}

func LoadPackages(directories ...string) (PackageResources, error) {
	resources := PackageResources{Providers: make(map[string]config.ProviderConfig)}
	for _, directory := range directories {
		installed, err := (packages.Manager{Directory: directory}).List()
		if err != nil {
			return resources, err
		}
		for _, entry := range installed {
			manifest, err := packages.ReadManifest(entry.Directory)
			if err != nil {
				return resources, fmt.Errorf("package %s: %w", entry.Name, err)
			}
			for name, profile := range manifest.Providers {
				if _, exists := resources.Providers[name]; exists {
					return resources, fmt.Errorf("duplicate package provider %s", name)
				}
				resources.Providers[name] = profile
			}
			resources.Skills = append(resources.Skills, manifest.Skills...)
			resources.Prompts = append(resources.Prompts, manifest.Prompts...)
			resources.Plugins = append(resources.Plugins, manifest.Plugins...)
			resources.Extensions = append(resources.Extensions, manifest.Extensions...)
			for event, resolved := range manifest.Hooks {
				resources.Hooks = append(resources.Hooks, Hook{Event: event, Path: resolved, Directory: entry.Directory})
			}
		}
	}
	return resources, nil
}

type HookResult struct {
	Block    bool            `json:"block"`
	Reason   string          `json:"reason"`
	Prompt   *string         `json:"prompt"`
	Messages json.RawMessage `json:"messages"`
}

func RunHook(ctx context.Context, hook Hook, event any) (HookResult, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return HookResult{}, err
	}
	result, err := process.Run(ctx, process.Options{Executable: hook.Path, Directory: hook.Directory, Input: strings.NewReader(string(data)), Timeout: 15 * time.Second, Environment: os.Environ()})
	if err != nil {
		if details := strings.TrimSpace(result.Stderr); details != "" {
			return HookResult{}, fmt.Errorf("extension %s failed: %w (details: %s)", filepath.Base(hook.Path), err, details)
		}
		return HookResult{}, fmt.Errorf("extension %s failed: %w", filepath.Base(hook.Path), err)
	}
	var response HookResult
	if strings.TrimSpace(result.Stdout) != "" {
		if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil {
			return response, fmt.Errorf("extension %s returned invalid JSON", filepath.Base(hook.Path))
		}
	}
	return response, nil
}
