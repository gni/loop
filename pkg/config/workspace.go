package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
		data, err := os.ReadFile(filepath.Join(trustDir, WorkspaceKey(canonical)+".json"))
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
	return writePrivateConfigFile(filepath.Join(filepath.Dir(configPath), "trust", WorkspaceKey(canonical)+".json"), data)
}

func LoadWorkspaceConfig(path, workspace string, trusted bool) (*Config, error) {
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	resolveConfigPaths(cfg, filepath.Dir(path))
	cfg.Sources = make(map[string]string)
	if data, readErr := os.ReadFile(path); readErr == nil {
		var values map[string]any
		if json.Unmarshal(data, &values) == nil {
			for key := range values {
				cfg.Sources[key] = path
			}
		}
	}
	cfg.DisableProjectPlugins = !trusted
	if !trusted {
		applyEnvironment(cfg)
		return cfg, nil
	}
	projectPath := filepath.Join(workspace, ".loop", "config.json")
	data, err := os.ReadFile(projectPath)
	if os.IsNotExist(err) {
		applyEnvironment(cfg)
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read project settings: %w", err)
	}
	baseConfig, err := Clone(cfg)
	if err != nil {
		return nil, err
	}
	base, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var merged, project map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("project settings: %w", err)
	}
	resolveProjectPaths(project, filepath.Dir(projectPath))
	for key := range project {
		cfg.Sources[key] = projectPath
	}
	mergeObjects(merged, project)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, cfg); err != nil {
		return nil, err
	}
	cfg.ProjectBase = baseConfig
	cfg.ProjectPath = projectPath
	cfg.DisableProjectPlugins = false
	cfg.applyDefaults()
	if err := cfg.ActivateProvider(cfg.ActiveProvider); err != nil {
		return nil, err
	}
	projectData, err := json.Marshal(project)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(projectData, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	applyEnvironment(cfg)
	return cfg, nil
}

func mergeObjects(target, source map[string]any) {
	for key, value := range source {
		child, nested := value.(map[string]any)
		existing, exists := target[key].(map[string]any)
		if nested && exists {
			mergeObjects(existing, child)
		} else {
			target[key] = value
		}
	}
}

func Clone(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var cloned Config
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	cloned.ProjectPath = cfg.ProjectPath
	cloned.ProjectBase = cfg.ProjectBase
	cloned.Sources = make(map[string]string, len(cfg.Sources))
	for key, source := range cfg.Sources {
		cloned.Sources[key] = source
	}
	return &cloned, nil
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
func applyEnvironment(cfg *Config) {
	for name, target := range map[string]*string{"LOOP_ENDPOINT": &cfg.Endpoint, "LOOP_MODEL": &cfg.Model} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			*target = value
			cfg.Sources[strings.ToLower(strings.TrimPrefix(name, "LOOP_"))] = "environment:" + name
		}
	}
	if _, ok := os.LookupEnv("LOOP_API_KEY"); ok {
		cfg.ApiKey = "env:LOOP_API_KEY"
		cfg.Sources["api_key"] = "environment:LOOP_API_KEY"
	}
	if raw, ok := os.LookupEnv("LOOP_TIMEOUT"); ok {
		if value, err := strconv.Atoi(raw); err == nil && value >= 0 {
			cfg.Timeout = value
			cfg.Sources["timeout"] = "environment:LOOP_TIMEOUT"
		}
	}
}
func saveProjectConfig(cfg *Config) error {
	if cfg.ProjectBase == nil {
		return fmt.Errorf("project configuration has no base settings")
	}
	currentData, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	baseData, err := json.Marshal(cfg.ProjectBase)
	if err != nil {
		return err
	}
	var current, base map[string]any
	if err := json.Unmarshal(currentData, &current); err != nil {
		return err
	}
	if err := json.Unmarshal(baseData, &base); err != nil {
		return err
	}
	patch := make(map[string]any)
	for key, value := range current {
		if !reflect.DeepEqual(value, base[key]) && key != "disable_project_plugins" {
			patch[key] = value
		}
	}
	for key := range base {
		if _, exists := current[key]; !exists && key != "disable_project_plugins" {
			patch[key] = nil
		}
	}
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateConfigFile(cfg.ProjectPath, data)
}
