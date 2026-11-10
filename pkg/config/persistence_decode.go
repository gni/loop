package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func decodeMCPFile(data []byte, path string, cfg *Config) error {
	var servers map[string]MCPServerConfig
	if err := json.Unmarshal(data, &servers); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if servers == nil {
		return fmt.Errorf("%s must contain a JSON object", path)
	}
	cfg.MCPServers = servers
	return nil
}

func decodeProvidersFile(data []byte, path string, cfg *Config) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if fields == nil {
		return fmt.Errorf("%s must contain a JSON object", path)
	}
	if _, wrapped := fields["providers"]; wrapped {
		var file ProvidersFile
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if file.Providers == nil {
			return fmt.Errorf("providers in %s must be a JSON object", path)
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
		if err := json.Unmarshal(data, &providers); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		cfg.Providers = providers
	}
	populateProviderNames(cfg.Providers)
	return nil
}

// A missing main settings file must not reset surviving provider credentials
// or MCP connections. Reject unreadable sidecars before creating any files.
func restoreConfigSidecars(snapshot configSnapshot, dir string, cfg *Config) error {
	for _, sidecar := range []struct {
		name   string
		file   configFileRead
		decode func([]byte, string, *Config) error
	}{
		{"mcp.json", snapshot.mcp, decodeMCPFile},
		{"providers.json", snapshot.providers, decodeProvidersFile},
	} {
		path := filepath.Join(dir, sidecar.name)
		if sidecar.file.err == nil {
			if err := sidecar.decode(sidecar.file.data, path, cfg); err != nil {
				return err
			}
		} else if !os.IsNotExist(sidecar.file.err) {
			return fmt.Errorf("read %s: %w", path, sidecar.file.err)
		}
	}
	return nil
}
