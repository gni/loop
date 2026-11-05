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
	cfg, rawGlobal, err := loadConfigWithRaw(path)
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
	var project map[string]any
	if err := json.Unmarshal(data, &project); err != nil {
		return nil, fmt.Errorf("project settings: %w", err)
	}
	resolveProjectPaths(project, filepath.Dir(projectPath))
	for key := range project {
		cfg.Sources[key] = projectPath
	}
	baseConfig, err := Clone(cfg)
	if err != nil {
		return nil, err
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
	// Preserve explicit top-level scalar overrides in .loop/config.json without
	// clobbering deep-merged Providers or MCPServers maps.
	if v, ok := project["endpoint"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.Endpoint = v
	}
	if v, ok := project["api_key"].(string); ok && strings.TrimSpace(v) != "" {
		if envVar, isEnv := parseEnvVarKey(v); isEnv {
			cfg.APIKey = os.Getenv(envVar)
		} else {
			cfg.APIKey = v
		}
	}
	if v, ok := project["model"].(string); ok && strings.TrimSpace(v) != "" {
		cfg.Model = v
	}
	if v, ok := project["timeout"].(float64); ok && int(v) > 0 {
		cfg.Timeout = int(v)
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
	cloned := *cfg
	if cfg.Nostop.IntervalSeconds != nil {
		sec := *cfg.Nostop.IntervalSeconds
		cloned.Nostop.IntervalSeconds = &sec
	}
	cloned.Sources = cloneStringMap(cfg.Sources)
	if cloned.Sources == nil {
		cloned.Sources = make(map[string]string)
	}
	if cfg.PluginDirectories != nil {
		cloned.PluginDirectories = append([]string(nil), cfg.PluginDirectories...)
	}
	if cfg.ExtensionDirectories != nil {
		cloned.ExtensionDirectories = append([]string(nil), cfg.ExtensionDirectories...)
	}
	if cfg.PromptDirectories != nil {
		cloned.PromptDirectories = append([]string(nil), cfg.PromptDirectories...)
	}
	if cfg.SkillDirectories != nil {
		cloned.SkillDirectories = append([]string(nil), cfg.SkillDirectories...)
	}
	if cfg.DisabledTools != nil {
		cloned.DisabledTools = append([]string(nil), cfg.DisabledTools...)
	}
	if cfg.RepeatReminderThresholds != nil {
		cloned.RepeatReminderThresholds = append([]int(nil), cfg.RepeatReminderThresholds...)
	}
	cloned.MCPServers = CopyMCPServers(cfg.MCPServers)
	cloned.Providers = CopyProviders(cfg.Providers)
	if cfg.Limits.ReasoningTokenBudgets != nil {
		budgets := make(map[string]int, len(cfg.Limits.ReasoningTokenBudgets))
		for k, v := range cfg.Limits.ReasoningTokenBudgets {
			budgets[k] = v
		}
		cloned.Limits.ReasoningTokenBudgets = budgets
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
		cfg.APIKey = "env:LOOP_API_KEY"
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
	sanitized, err := Clone(cfg)
	if err != nil {
		return err
	}
	if profile, ok := sanitized.Providers[sanitized.ActiveProvider]; ok {
		if _, isEnv := parseEnvVarKey(profile.APIKey); isEnv {
			sanitized.APIKey = profile.APIKey
		}
	}
	currentData, err := json.Marshal(sanitized)
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
		if key == "disable_project_plugins" {
			continue
		}
		if key == "api_key" || key == "providers" || key == "mcp_servers" {
			if cfg.Sources[key] != cfg.ProjectPath {
				continue
			}
			if key == "api_key" {
				if str, ok := value.(string); ok && str != "" {
					if _, isEnv := parseEnvVarKey(str); isEnv {
						patch[key] = str
					}
				}
				continue
			}
			if key == "mcp_servers" {
				baseServers, _ := base["mcp_servers"].(map[string]any)
				if currentServers, ok := value.(map[string]any); ok {
					projectServers := make(map[string]any)
					for name, serverDef := range currentServers {
						// Never persist servers inherited from global base config into repository
						if _, inBase := baseServers[name]; !inBase {
							projectServers[name] = serverDef
						}
					}
					if len(projectServers) > 0 {
						patch["mcp_servers"] = projectServers
					}
				}
				continue
			}
			if key == "providers" {
				baseProviders, _ := base["providers"].(map[string]any)
				if currentProviders, ok := value.(map[string]any); ok {
					projectProviders := make(map[string]any)
					for name, providerDef := range currentProviders {
						// Never persist providers inherited from global base config into repository
						if _, inBase := baseProviders[name]; !inBase {
							if pMap, ok := providerDef.(map[string]any); ok {
								// Strip raw API keys; only preserve env references
								if rawKey, hasKey := pMap["api_key"].(string); hasKey && rawKey != "" {
									if _, isEnv := parseEnvVarKey(rawKey); !isEnv {
										delete(pMap, "api_key")
									}
								}
								projectProviders[name] = pMap
							} else {
								projectProviders[name] = providerDef
							}
						}
					}
					if len(projectProviders) > 0 {
						patch["providers"] = projectProviders
					}
				}
				continue
			}
		}
		if !reflect.DeepEqual(value, base[key]) {
			patch[key] = value
		}
	}
	for key := range base {
		if key == "disable_project_plugins" || key == "api_key" || key == "providers" || key == "mcp_servers" {
			continue
		}
		if _, exists := current[key]; !exists {
			patch[key] = nil
		}
	}
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateConfigFile(cfg.ProjectPath, data)
}
