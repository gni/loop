package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"loop/pkg/storage"
)

func encodeMCPFile(servers map[string]MCPServerConfig) ([]byte, error) {
	if servers == nil {
		servers = make(map[string]MCPServerConfig)
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode MCP configuration: %w", err)
	}
	return data, nil
}

func saveMCPFile(ctx context.Context, dir string, servers map[string]MCPServerConfig) error {
	data, err := encodeMCPFile(servers)
	if err != nil {
		return err
	}
	return writeInitialConfigFile(ctx, filepath.Join(dir, "mcp.json"), data)
}

func encodeProvidersFile(active string, providers map[string]ProviderConfig) ([]byte, error) {
	clean := make(map[string]ProviderConfig, len(providers))
	for name, p := range providers {
		p.Name = ""
		clean[name] = p
	}
	data, err := json.MarshalIndent(ProvidersFile{Active: active, ActiveProvider: active, Providers: clean}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode provider configuration: %w", err)
	}
	return data, nil
}

func saveProvidersFile(ctx context.Context, dir, active string, providers map[string]ProviderConfig) error {
	data, err := encodeProvidersFile(active, providers)
	if err != nil {
		return err
	}
	return writeInitialConfigFile(ctx, filepath.Join(dir, "providers.json"), data)
}

func writePrivateConfigFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	f, err := os.CreateTemp(dir, ".loop-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fmt.Errorf("set private config permissions: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func populateProviderNames(providers map[string]ProviderConfig) {
	for name, p := range providers {
		if p.Name == "" {
			p.Name = name
			providers[name] = p
		}
	}
}

func LoadConfig(path string) (*Config, error) { return LoadConfigContext(context.Background(), path) }

// LoadConfigContext allows cancellation while waiting for configuration storage.
func LoadConfigContext(ctx context.Context, path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	cfg, _, err := loadConfigWithRaw(ctx, path)
	return cfg, err
}

func loadConfigWithRaw(ctx context.Context, path string) (*Config, []byte, error) {
	snapshot, err := readConfigSnapshot(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	if os.IsNotExist(snapshot.settings.err) {
		conf := DefaultConfig()

		if len(embeddedDefaultProviders) > 0 {
			var provFile ProvidersFile
			if err := json.Unmarshal(embeddedDefaultProviders, &provFile); err == nil && provFile.Providers != nil {
				conf.Providers = provFile.Providers
				if provFile.ActiveProvider != "" {
					conf.ActiveProvider = provFile.ActiveProvider
				}
			}
		}

		if len(embeddedDefaultMCP) > 0 {
			var mcpMap map[string]MCPServerConfig
			if err := json.Unmarshal(embeddedDefaultMCP, &mcpMap); err == nil && mcpMap != nil {
				conf.MCPServers = mcpMap
			}
		}

		conf.applyDefaults()
		conf.ensureDefaultProvider()
		if err := conf.SyncActiveProvider(); err != nil {
			return nil, nil, err
		}
		if err := saveConfig(ctx, path, conf, true); err != nil {
			if errors.Is(err, errConfigExists) {
				return loadConfigWithRaw(ctx, path)
			}
			return nil, nil, err
		}
		if err := applyTimeoutEnvironment(conf); err != nil {
			return nil, nil, err
		}
		return conf, nil, nil
	}

	data, err := snapshot.settings.data, snapshot.settings.err
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	applyLegacyDefaults(data, cfg)
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}
	cfg.applyDefaults()

	dir := filepath.Dir(path)
	mcpPath := filepath.Join(dir, "mcp.json")
	if mcpData, err := snapshot.mcp.data, snapshot.mcp.err; err == nil {
		var servers map[string]MCPServerConfig
		if err := json.Unmarshal(mcpData, &servers); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", mcpPath, err)
		}
		if servers == nil {
			return nil, nil, fmt.Errorf("%s must contain a JSON object", mcpPath)
		}
		cfg.MCPServers = servers
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("read %s: %w", mcpPath, err)
	} else if len(cfg.MCPServers) > 0 {
		if err := saveMCPFile(ctx, dir, cfg.MCPServers); err != nil {
			return nil, nil, err
		}
	}

	providersPath := filepath.Join(dir, "providers.json")
	if provData, err := snapshot.providers.data, snapshot.providers.err; err == nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(provData, &fields); err != nil {
			return nil, nil, fmt.Errorf("parse %s: %w", providersPath, err)
		}
		if fields == nil {
			return nil, nil, fmt.Errorf("%s must contain a JSON object", providersPath)
		}
		if _, wrapped := fields["providers"]; wrapped {
			var file ProvidersFile
			if err := json.Unmarshal(provData, &file); err != nil {
				return nil, nil, fmt.Errorf("parse %s: %w", providersPath, err)
			}
			if file.Providers == nil {
				return nil, nil, fmt.Errorf("providers in %s must be a JSON object", providersPath)
			}
			active := file.Active
			if active == "" {
				active = file.ActiveProvider
			}
			if active != "" {
				cfg.ActiveProvider = active
			}
			cfg.Providers = file.Providers
		} else {
			var providers map[string]ProviderConfig
			if err := json.Unmarshal(provData, &providers); err != nil {
				return nil, nil, fmt.Errorf("parse %s: %w", providersPath, err)
			}
			cfg.Providers = providers
		}
		populateProviderNames(cfg.Providers)
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("read %s: %w", providersPath, err)
	} else if len(cfg.Providers) > 0 {
		if err := saveProvidersFile(ctx, dir, cfg.ActiveProvider, cfg.Providers); err != nil {
			return nil, nil, err
		}
	} else if cfg.Endpoint != "" {
		providerName := cfg.ActiveProvider
		if providerName == "" {
			providerName = "default"
			cfg.ActiveProvider = "default"
		}
		cfg.Providers[providerName] = cfg.defaultProviderProfile(providerName)
		if err := saveProvidersFile(ctx, dir, cfg.ActiveProvider, cfg.Providers); err != nil {
			return nil, nil, err
		}
	}

	if err := cfg.ActivateProvider(cfg.ActiveProvider); err != nil {
		return nil, nil, err
	}
	cfg.NormalizeSafety()
	if err := applyTimeoutEnvironment(cfg); err != nil {
		return nil, nil, err
	}

	return cfg, data, nil
}

// DefaultConfigPath returns the canonical path to the default user configuration file.
func DefaultConfigPath() string {
	if dir := os.Getenv("LOOP_AGENT_DIR"); dir != "" {
		return filepath.Join(dir, "config.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".loop", "config.json")
	}
	return "config/config.json"
}

func SaveConfig(path string, cfg *Config) error {
	return SaveConfigContext(context.Background(), path, cfg)
}

// SaveConfigContext cancels lock acquisition before a save starts. Once a
// journal is prepared, commit or recovery runs to a consistent boundary.
func SaveConfigContext(ctx context.Context, path string, cfg *Config) error {
	return saveConfig(ctx, path, cfg, false)
}

func saveConfig(ctx context.Context, path string, cfg *Config, createOnly bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := cfg.ValidateSettings(); err != nil {
		return err
	}

	copy, err := Clone(cfg)
	if err != nil {
		return err
	}
	cfg = copy
	cfg.NormalizeSafety()
	if path == "" {
		path = DefaultConfigPath()
	}
	if cfg.ProjectPath != "" && filepath.Clean(path) == filepath.Clean(cfg.ProjectPath) {
		return saveProjectConfig(ctx, cfg)
	}
	base := filepath.Base(path)
	if !validConfigMainName(base) {
		return fmt.Errorf("global config path conflicts with reserved file %s", base)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg.UpdateActiveProvider()
	cfg.ensureDefaultProvider()

	savedMCP := cfg.MCPServers
	savedProviders := cfg.Providers
	cfg.MCPServers = nil
	cfg.Providers = nil

	if profile, ok := savedProviders[cfg.ActiveProvider]; ok {
		if _, env := parseEnvVarKey(profile.APIKey); env {
			cfg.APIKey = profile.APIKey
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")

	cfg.MCPServers = savedMCP
	cfg.Providers = savedProviders

	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	mcpData, err := encodeMCPFile(cfg.MCPServers)
	if err != nil {
		return err
	}
	providerData, err := encodeProvidersFile(cfg.ActiveProvider, cfg.Providers)
	if err != nil {
		return err
	}
	return writeConfigBundle(ctx, []configFileUpdate{
		{path: path, data: data},
		{path: filepath.Join(dir, "mcp.json"), data: mcpData},
		{path: filepath.Join(dir, "providers.json"), data: providerData},
	}, createOnly)
}

func WritePrivateFile(path string, data []byte) error {
	if err := writePrivateConfigFile(path, data); err != nil {
		return err
	}
	return storage.SyncDirectory(filepath.Dir(path))
}
