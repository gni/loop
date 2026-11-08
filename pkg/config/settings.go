package config

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	return json.Unmarshal(normalized, (*plain)(cfg))
}

func ParseBoolValue(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("expected true or false, got %q", value)
	}
}

func settingField(key string) (reflect.StructField, bool) {
	t := reflect.TypeOf(Config{})
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if strings.Split(f.Tag.Get("json"), ",")[0] == key && key != "-" {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func SettingKeys() []string {
	t := reflect.TypeOf(Config{})
	keys := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		key := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if key != "" && key != "-" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// CanonicalSetting validates booleans before any setting or side effect changes.
func CanonicalSetting(key, value string) (string, string, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	aliases := map[string]string{
		"yes": "auto_approve", "yolo": "auto_approve", "guided": "guided_mode", "always_answer": "auto_approve",
		"thinking": "show_thinking", "markdown": "markdown_render", "collapse": "collapse_results", "tokens": "show_tokens", "microseconds": "show_microseconds",
		"auto_adapt": "auto_adapt_context", "direct": "direct_commands", "stream": "stream_writes", "stream_write": "stream_writes", "persistent_shell": "persistent_bash", "atomic_write": "atomic_writes", "file_guard_off": "disable_file_observation_guard", "skip": "skip_verify",
		"url": "endpoint", "key": "api_key", "temp": "temperature", "reasoning": "reasoning_effort", "before_hook": "before_tool_hook", "after_hook": "after_tool_hook",
		"page_size": "history_page_size", "history_chunk": "history_page_size", "history_lines": "history_page_size", "syntax": "syntax_theme",
		"context_limit": "context_window_limit", "context": "context_window_limit", "min_context": "min_context_window", "compress_threshold": "compression_threshold", "compression_ratio": "compression_threshold",
		"compaction_reserve": "compaction_reserve_tokens", "reserve_tokens": "compaction_reserve_tokens", "max_tokens": "max_completion_tokens", "output_tokens": "max_completion_tokens", "max_steps": "max_reasoning_steps", "steps": "max_reasoning_steps",
		"cert": "cert_file", "client_key": "key_file", "ca": "ca_file", "debug": "debug_log_file", "paste_lines": "max_paste_lines", "paste_threshold": "max_paste_lines", "paste_chars": "max_paste_chars", "llm_timeout": "timeout", "spill_limit": "max_tool_output_bytes", "guard_limit": "repeat_guard_limit",
		"prompts": "prompts_file", "ask_user": "ask_user_mode", "disable_tools": "disabled_tools", "disable_tool": "disabled_tools",
	}
	if alias, ok := aliases[key]; ok {
		key = alias
	}
	invert := false
	if alias, ok := legacyBooleans[key]; ok {
		key, invert = alias.name, alias.invert
	}
	if field, ok := settingField(key); ok && field.Type.Kind() == reflect.Bool {
		enabled, err := ParseBoolValue(value)
		if err != nil {
			return "", "", err
		}
		if invert {
			enabled = !enabled
		}
		value = strconv.FormatBool(enabled)
	}
	return key, value, nil
}

// SetValue handles the complete persisted settings schema, including JSON lists
// and objects. Specialized UI handlers still own immediate runtime side effects.
func (cfg *Config) SetValue(key, value string) error {
	key, value, err := CanonicalSetting(key, value)
	if err != nil {
		return err
	}
	field, ok := settingField(key)
	if !ok {
		return fmt.Errorf("unknown config key: %s", key)
	}
	target := reflect.New(field.Type)
	switch field.Type.Kind() {
	case reflect.String:
		switch key {
		case "tool_protocol":
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "json" && value != "xml" {
				return fmt.Errorf("tool_protocol must be json or xml")
			}
		case "reasoning_effort":
			var err error
			value, err = ParseReasoningEffort(value)
			if err != nil {
				return err
			}
		case "ask_user_mode":
			mode, err := ParseAskUserMode(value)
			if err != nil {
				return err
			}
			cfg.SetAskUserMode(mode)
			return nil
		case "active_provider":
			return cfg.ActivateProvider(value)
		}
		target.Elem().SetString(value)
	case reflect.Bool:
		enabled, err := ParseBoolValue(value)
		if err != nil {
			return err
		}
		if key == "auto_approve" && enabled {
			cfg.GuidedMode = false
		}
		target.Elem().SetBool(enabled)
	case reflect.Int:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be an integer", key)
		}
		if key == "logo_brightness" && (n < 10 || n > 100) {
			return fmt.Errorf("logo_brightness must be between 10 and 100")
		}
		if key != "recap_interval" && n < 0 {
			return fmt.Errorf("%s must be non-negative", key)
		}
		if n == 0 {
			switch key {
			case "timeout", "shell_timeout", "extension_timeout", "max_subagent_depth":
			default:
				return fmt.Errorf("%s must be non-zero", key)
			}
		}
		target.Elem().SetInt(n)
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s must be a finite number", key)
		}
		if key == "temperature" && (n < 0 || n > 2) {
			return fmt.Errorf("temperature must be between 0 and 2")
		}
		if (key == "frequency_penalty" || key == "presence_penalty") && (n < -2 || n > 2) {
			return fmt.Errorf("%s must be between -2 and 2", key)
		}
		target.Elem().SetFloat(n)
	default:
		if err := json.Unmarshal([]byte(value), target.Interface()); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	reflect.ValueOf(cfg).Elem().FieldByIndex(field.Index).Set(target.Elem())
	return nil
}

// MCP servers were historically enabled unless explicitly disabled.
func (server *MCPServerConfig) UnmarshalJSON(data []byte) error {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
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

// SettingValue returns an editable value. Callers must redact secrets for display.
func (cfg *Config) SettingValue(key string) (string, error) {
	field, ok := settingField(key)
	if !ok {
		return "", fmt.Errorf("unknown config key: %s", key)
	}
	value := reflect.ValueOf(cfg).Elem().FieldByIndex(field.Index)
	if value.Kind() == reflect.String {
		return value.String(), nil
	}
	data, err := json.Marshal(value.Interface())
	return string(data), err
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
