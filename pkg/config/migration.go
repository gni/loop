package config

import (
	"encoding/json"
	"fmt"
)

type legacyBoolean struct {
	name   string
	invert bool
}

var legacyBooleans = map[string]legacyBoolean{
	"disable_local_plugins":          {"local_plugins", true},
	"disable_file_observation_guard": {"file_observation_guard", true},
	"disable_recap":                  {"recap", true},
	"no_context_files":               {"context_files", true},
	"skip_verify":                    {"tls_verify", true},
	"approval_always_answer":         {"auto_approve", false},
	"enable_mcp":                     {"mcp", false},
}

// NormalizeConfigJSON migrates old names before overlaying project settings.
// Explicit new names take precedence; saves only write the new names.
func NormalizeConfigJSON(data []byte) ([]byte, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, fmt.Errorf("settings must contain a JSON object")
	}

	for old, alias := range legacyBooleans {
		raw, ok := values[old]
		if !ok {
			continue
		}
		if _, exists := values[alias.name]; !exists && string(raw) != "null" {
			var enabled bool
			if err := json.Unmarshal(raw, &enabled); err != nil {
				return nil, fmt.Errorf("%s: %w", old, err)
			}
			if alias.invert {
				enabled = !enabled
			}
			values[alias.name], _ = json.Marshal(enabled)
		}
		delete(values, old)
	}
	// Workspace resource permission comes from trust, never from a config file.
	delete(values, "disable_project_plugins")
	delete(values, "project_resources")
	return json.Marshal(values)
}

func (cfg *Config) UnmarshalJSON(data []byte) error {
	normalized, err := NormalizeConfigJSON(data)
	if err != nil {
		return err
	}
	type plain Config
	var incoming plain
	if err := json.Unmarshal(normalized, &incoming); err != nil {
		return err
	}
	if err := (*Config)(&incoming).ValidateSettings(); err != nil {
		return err
	}
	if err := json.Unmarshal(normalized, (*plain)(cfg)); err != nil {
		return err
	}
	cfg.normalizeValues()
	return nil
}

// MCP servers were historically enabled unless explicitly disabled.
func (server *MCPServerConfig) UnmarshalJSON(data []byte) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		return fmt.Errorf("MCP server configuration must contain a JSON object")
	}
	if raw, ok := values["alwaysLoad"]; ok {
		if _, exists := values["always_load"]; !exists {
			values["always_load"] = raw
		}
		delete(values, "alwaysLoad")
	}
	if raw, ok := values["disabled"]; ok {
		if _, exists := values["enabled"]; !exists {
			var disabled bool
			if err := json.Unmarshal(raw, &disabled); err != nil {
				return err
			}
			values["enabled"], _ = json.Marshal(!disabled)
		}
		delete(values, "disabled")
	}
	normalized, err := json.Marshal(values)
	if err != nil {
		return err
	}
	type plain MCPServerConfig
	decoded := plain{Enabled: true}
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		return err
	}
	*server = MCPServerConfig(decoded)
	return nil
}

func applyLegacyDefaults(data []byte, cfg *Config) {
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil {
		return
	}
	// Old files omitted disable_local_plugins when plugins were allowed.
	legacyFormat := false
	for key := range legacyBooleans {
		if _, ok := values[key]; ok {
			legacyFormat = true
			break
		}
	}
	if legacyFormat {
		_, oldPresent := values["disable_local_plugins"]
		_, newPresent := values["local_plugins"]
		if !oldPresent && !newPresent {
			values["local_plugins"] = json.RawMessage("true")
		}
	}
	if raw, ok := values["local_plugins"]; ok {
		_ = json.Unmarshal(raw, &cfg.LocalPlugins)
	}
}
