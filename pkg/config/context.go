package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
)

const (
	DefaultContextWindowLimit  = 8192
	DefaultMinContextWindow    = 8192
	DefaultMaxCompletionTokens = 2048
)

type contextProfile struct {
	window int
	output int
}

func parseContextProfile(name string) (string, contextProfile, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	profiles := map[string]contextProfile{
		"8k": {8192, 2048}, "16k": {16384, 4096}, "32k": {32768, 8192},
		"64k": {65536, 8192}, "128k": {131072, 8192}, "256k": {262144, 8192},
		"512k": {524288, 8192}, "1m": {1048576, 8192},
	}
	if name == "" || name == "custom" {
		return name, contextProfile{}, nil
	}
	profile, ok := profiles[strings.TrimPrefix(name, "local-")]
	if !ok {
		return "", contextProfile{}, fmt.Errorf("context_profile must be 8k, 16k, 32k, 64k, 128k, 256k, 512k, 1m, custom, or a local- size preset")
	}
	return name, profile, nil
}

// Size presets change context defaults. local- presets also group existing
// efficiency controls; explicit configuration-file fields always take priority.
func (cfg *Config) ApplyContextProfile(name string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	name, profile, err := parseContextProfile(name)
	if err != nil {
		return err
	}
	next := *cfg
	next.ContextProfile = name
	if profile.window > 0 {
		next.ContextWindowLimit = profile.window
		next.MinContextWindow = min(profile.window, DefaultMinContextWindow)
		next.MaxCompletionTokens = profile.output
	}
	if strings.HasPrefix(name, "local-") {
		next.ChatCapabilities.MaxTokens = true
		next.AutoAdaptContext, next.AdaptiveSteps, next.CompactPrompt = true, true, true
		next.ToolExposure, next.HistoryCompression, next.MemoryPolicy = "core", "safe", "auto"
		next.MaxSubagentDepth = 0
		next.MaxToolOutputBytes = min(8192, profile.window/2)
		next.SubagentTimeout, next.SubagentTokenBudget = 300, profile.window*4
	}
	if err := next.ValidateSettings(); err != nil {
		return err
	}
	*cfg = next
	return nil
}

func expandContextProfile(values map[string]json.RawMessage) error {
	raw, exists := values["context_profile"]
	if !exists {
		return nil
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return fmt.Errorf("context_profile: %w", err)
	}
	name, profile, err := parseContextProfile(name)
	if err != nil {
		return err
	}
	values["context_profile"], _ = json.Marshal(name)
	if profile.window == 0 {
		return nil
	}
	defaults := map[string]any{
		"context_window_limit":  profile.window,
		"min_context_window":    min(profile.window, DefaultMinContextWindow),
		"max_completion_tokens": profile.output,
	}
	if strings.HasPrefix(name, "local-") {
		maps.Copy(defaults, map[string]any{"chat_capabilities": ChatCapabilities{MaxTokens: true}, "auto_adapt_context": true, "adaptive_steps": true, "compact_prompt": true, "tool_exposure": "core", "history_compression": "safe", "memory_policy": "auto", "max_subagent_depth": 0, "max_tool_output_bytes": min(8192, profile.window/2), "subagent_timeout": 300, "subagent_token_budget": profile.window * 4})
	}
	for key, value := range defaults {
		if raw, explicit := values[key]; !explicit || strings.TrimSpace(string(raw)) == "null" {
			values[key], _ = json.Marshal(value)
		}
	}
	return nil
}
