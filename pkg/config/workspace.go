package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/storage"
)

func CanonicalWorkspace(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory: %s", path)
	}
	return resolved, nil
}

func WorkspaceKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:16])
}

// WorkspaceTrusted inherits the nearest saved decision along the canonical
// directory ancestry. Explicit refusals override a trusted parent, and symlinks
// are checked at their real destination rather than their apparent location.
func WorkspaceTrusted(configPath, workspace string) bool {
	canonical, err := CanonicalWorkspace(workspace)
	if err != nil {
		return false
	}
	trustDir := filepath.Join(filepath.Dir(configPath), "trust")
	for {
		data, err := storage.ReadFile(filepath.Join(trustDir, WorkspaceKey(canonical)+".json"), 16<<10)
		if err == nil {
			var decision struct {
				Workspace string `json:"workspace"`
				Trusted   bool   `json:"trusted"`
			}
			return json.Unmarshal(data, &decision) == nil && decision.Trusted && decision.Workspace == canonical
		}
		if !os.IsNotExist(err) {
			return false
		}
		parent := filepath.Dir(canonical)
		if parent == canonical {
			return false
		}
		canonical = parent
	}
}

func TrustWorkspace(configPath, workspace string, trusted bool) error {
	canonical, err := CanonicalWorkspace(workspace)
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"workspace": canonical, "trusted": trusted})
	if err != nil {
		return err
	}
	return WritePrivateFile(filepath.Join(filepath.Dir(configPath), "trust", WorkspaceKey(canonical)+".json"), data)
}

func LoadWorkspaceConfig(path, workspace string, trusted bool) (*Config, error) {
	return LoadWorkspaceConfigContext(context.Background(), path, workspace, trusted)
}

// LoadWorkspaceConfigContext applies workspace overrides after a cancellable global load.
func LoadWorkspaceConfigContext(ctx context.Context, path, workspace string, trusted bool) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	cfg, rawGlobal, err := loadConfigWithRaw(ctx, path)
	if err != nil {
		return nil, err
	}
	resolveConfigPaths(cfg, filepath.Dir(path))
	cfg.Sources = make(map[string]string)
	if len(rawGlobal) > 0 {
		var values map[string]any
		if json.Unmarshal(rawGlobal, &values) == nil {
			for key := range values {
				cfg.Sources[key] = path
			}
		}
	}
	cfg.ProjectResources = trusted
	if !trusted {
		if err := applyEnvironment(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	projectPath := filepath.Join(workspace, ".loop", "config.json")
	if sameConfigPath(path, projectPath) {
		if err := applyEnvironment(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	data, err := storage.ReadFileContext(ctx, projectPath, 8<<20)
	if os.IsNotExist(err) {
		if err := applyEnvironment(cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project settings: %w", err)
	}
	data, err = NormalizeConfigJSON(data)
	if err != nil {
		return nil, fmt.Errorf("project settings: %w", err)
	}
	var project map[string]any
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("project settings: %w", err)
	}
	resolveProjectPaths(project, filepath.Dir(projectPath))
	baseConfig, err := Clone(cfg)
	if err != nil {
		return nil, err
	}
	for key := range project {
		cfg.Sources[key] = projectPath
	}
	base, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	mergeObjects(merged, project)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	sources := cfg.Sources
	cfg = new(Config)
	if err := json.Unmarshal(encoded, cfg); err != nil {
		return nil, err
	}
	cfg.Sources = sources
	cfg.ProjectBase = baseConfig
	cfg.ProjectPath = projectPath
	cfg.ProjectResources = true
	cfg.applyDefaults()
	if err := cfg.ActivateProvider(cfg.ActiveProvider); err != nil {
		return nil, err
	}
	// Preserve explicit top-level scalar overrides in .loop/config.json without
	// clobbering deep-merged Providers or MCPServers maps.
	if v, ok := project["endpoint"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.Endpoint = v
	}
	if v, ok := project["api_key"].(string); ok {
		cfg.APIKey = v
	} else if value, exists := project["api_key"]; exists && value == nil {
		cfg.APIKey = ""
	}
	if v, ok := project["model"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.Model = v
	}
	if v, ok := project["timeout"].(float64); ok && int(v) >= 0 {
		cfg.Timeout = int(v)
	}
	if err := applyEnvironment(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func sameConfigPath(a, b string) bool {
	canonical := func(path string) (string, error) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		return absolute, nil
	}
	left, err := canonical(a)
	if err != nil {
		return false
	}
	right, err := canonical(b)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(left, right)
	return err == nil && relative == "."
}

func mergeObjects(target, source map[string]any) {
	for key, value := range source {
		if value == nil {
			delete(target, key)
			continue
		}
		child, nested := value.(map[string]any)
		existing, exists := target[key].(map[string]any)
		if nested && exists {
			mergeObjects(existing, child)
		} else {
			target[key] = value
		}
	}
}

func absoluteSetting(base, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return filepath.Join(base, path)
}
func resolveConfigPaths(cfg *Config, base string) {
	for _, path := range []*string{&cfg.SkillsDir, &cfg.CertFile, &cfg.KeyFile, &cfg.CAFile, &cfg.PromptsFile} {
		*path = absoluteSetting(base, *path)
	}
	for _, paths := range [][]string{cfg.SkillDirectories, cfg.PluginDirectories, cfg.PromptDirectories, cfg.ExtensionDirectories} {
		for i, path := range paths {
			paths[i] = absoluteSetting(base, path)
		}
	}
	for name, server := range cfg.MCPServers {
		if strings.Contains(server.Command, string(filepath.Separator)) {
			server.Command = absoluteSetting(base, server.Command)
			cfg.MCPServers[name] = server
		}
	}
}
func resolveProjectPaths(values map[string]any, base string) {
	for _, key := range []string{"skills_dir", "cert_file", "key_file", "ca_file", "prompts_file"} {
		if path, ok := values[key].(string); ok {
			values[key] = absoluteSetting(base, path)
		}
	}
	for _, key := range []string{"skill_directories", "plugin_directories", "prompt_directories", "extension_directories"} {
		if paths, ok := values[key].([]any); ok {
			for i, path := range paths {
				if text, ok := path.(string); ok {
					paths[i] = absoluteSetting(base, text)
				}
			}
		}
	}
	if servers, ok := values["mcp_servers"].(map[string]any); ok {
		for _, value := range servers {
			if server, ok := value.(map[string]any); ok {
				if command, ok := server["command"].(string); ok && strings.Contains(command, "/") {
					server["command"] = absoluteSetting(base, command)
				}
			}
		}
	}
}
func applyEnvironment(cfg *Config) error {
	for name, target := range map[string]*string{"LOOP_ENDPOINT": &cfg.Endpoint, "LOOP_MODEL": &cfg.Model} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			*target = value
			cfg.Sources[strings.ToLower(strings.TrimPrefix(name, "LOOP_"))] = "environment:" + name
		}
	}
	if _, ok := os.LookupEnv("LOOP_API_KEY"); ok {
		cfg.APIKey = "env:LOOP_API_KEY"
		cfg.Sources["api_key"] = "environment:LOOP_API_KEY"
	}
	if err := applyTimeoutEnvironment(cfg); err != nil {
		return err
	}
	if os.Getenv("LOOP_TIMEOUT") != "" {
		cfg.Sources["timeout"] = "environment:LOOP_TIMEOUT"
	}
	return nil
}

func applyTimeoutEnvironment(cfg *Config) error {
	if raw := os.Getenv("LOOP_TIMEOUT"); raw != "" {
		if err := cfg.SetValue("timeout", raw); err != nil {
			return fmt.Errorf("LOOP_TIMEOUT: %w", err)
		}
	}
	return nil
}
