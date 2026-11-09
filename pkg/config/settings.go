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

// isAskUserToolName covers the aliases recognized by the tool registry.
func isAskUserToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "ask_user", "ask_user_question", "askuser", "ask_human", "question_user", "ask", "ask_question", "confirm":
		return true
	}
	return false
}

// SetAskUserMode keeps the mode authoritative over the legacy disabled_tools entry.
func (c *Config) SetAskUserMode(mode string) {
	if c == nil {
		return
	}
	c.AskUserMode = mode
	filtered := c.DisabledTools[:0]
	for _, name := range c.DisabledTools {
		if !isAskUserToolName(name) {
			filtered = append(filtered, name)
		}
	}
	c.DisabledTools = filtered
}

// NormalizeSafety canonicalizes accepted values, enforces guided mode and
// migrates legacy ask_user tool disabling.
func (c *Config) NormalizeSafety() {
	if c == nil {
		return
	}
	c.normalizeValues()
	for _, name := range c.DisabledTools {
		if isAskUserToolName(name) {
			c.SetAskUserMode("disabled")
			break
		}
	}
	if c.GuidedMode {
		c.AutoApprove = false

		c.ParallelToolCalls = false
	}
}

// Keep accepted aliases identical for files, flags and runtime configuration.
func (cfg *Config) normalizeValues() {
	if effort, err := ParseReasoningEffort(cfg.ReasoningEffort); err == nil {
		cfg.ReasoningEffort = effort
	}
	if mode, err := ParseAskUserMode(cfg.AskUserMode); err == nil {
		cfg.AskUserMode = mode
	}
	cfg.ToolProtocol = strings.ToLower(strings.TrimSpace(cfg.ToolProtocol))
	if cfg.CompressionThreshold > 0 {
		if value, err := ParseCompressionThreshold(strconv.FormatFloat(cfg.CompressionThreshold, 'g', -1, 64)); err == nil {
			cfg.CompressionThreshold = value
		}
	}
}

type settingDescriptor struct {
	key   string
	field reflect.StructField
}

// Build schema metadata once; configuration values remain instance-owned.
var settingsSchema = func() []settingDescriptor {
	t := reflect.TypeOf(Config{})
	fields := make([]settingDescriptor, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		key := strings.Split(field.Tag.Get("json"), ",")[0]
		if key != "" && key != "-" {
			fields = append(fields, settingDescriptor{key: key, field: field})
		}
	}
	return fields
}()

var settingsByKey = func() map[string]reflect.StructField {
	fields := make(map[string]reflect.StructField, len(settingsSchema))
	for _, setting := range settingsSchema {
		fields[setting.key] = setting.field
	}
	return fields
}()

func settingField(key string) (reflect.StructField, bool) {
	field, ok := settingsByKey[key]
	return field, ok
}

func SettingKeys() []string {
	keys := make([]string, 0, len(settingsSchema))
	for _, setting := range settingsSchema {
		keys = append(keys, setting.key)
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
		target.Elem().SetBool(enabled)
	case reflect.Int:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || target.Elem().OverflowInt(n) {
			return fmt.Errorf("%s must be an integer", key)
		}
		if key == "timeout" || key == "shell_timeout" || key == "extension_timeout" {
			if err := validateTimeoutValue(key, n); err != nil {
				return err
			}
		}
		if n == 0 {
			switch key {
			case "timeout", "shell_timeout", "extension_timeout", "max_subagent_depth":
			default:
				n = reflect.ValueOf(DefaultConfig()).Elem().FieldByIndex(field.Index).Int()
			}
		}
		target.Elem().SetInt(n)
	case reflect.Float64:
		n, err := strconv.ParseFloat(value, 64)
		if key == "compression_threshold" {
			n, err = ParseCompressionThreshold(value)
		}
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return fmt.Errorf("%s must be a finite number", key)
		}
		target.Elem().SetFloat(n)
	default:
		if err := json.Unmarshal([]byte(value), target.Interface()); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	candidate := *cfg
	reflect.ValueOf(&candidate).Elem().FieldByIndex(field.Index).Set(target.Elem())
	if err := candidate.ValidateSettings(); err != nil {
		return err
	}
	reflect.ValueOf(cfg).Elem().FieldByIndex(field.Index).Set(target.Elem())
	if key == "auto_approve" && cfg.AutoApprove {
		cfg.GuidedMode = false
	}
	cfg.NormalizeSafety()
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
