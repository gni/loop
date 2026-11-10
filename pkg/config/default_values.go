package config

import (
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/domain/limits"
)

const (
	defaultMaxReasoningSteps   = 100
	defaultMaxCompletionTokens = 8192
	defaultContextWindowLimit  = 128000
	defaultMinContextWindow    = 32768
)

func resolveDefaultSkillsDir() string {
	if _, err := os.Stat("/workspace/agent/skills"); err == nil {
		return "/workspace/agent/skills"
	}
	if _, err := os.Stat("skills"); err == nil {
		return "skills"
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".loop", "skills")
}

func DefaultConfig() *Config {
	rawEnvKey := os.Getenv("OPENAI_API_KEY")
	apiKey := ""
	if rawEnvKey != "" {
		apiKey = "env:OPENAI_API_KEY"
	}
	endpoint := os.Getenv("OPENAI_API_BASE")
	if endpoint == "" {
		endpoint = "http://localhost:8080"
	}

	model := "llama-3-instruct"
	if rawEnvKey != "" && os.Getenv("OPENAI_API_BASE") == "" {
		endpoint = "http://openai-compatible.local"
		model = "gpt-4-turbo"
	}

	return &Config{
		ContextFiles:             true,
		FileObservationGuard:     true,
		TLSVerify:                true,
		ProjectResources:         true,
		PromptMaxLines:           8,
		Endpoint:                 endpoint,
		APIKey:                   apiKey,
		Model:                    model,
		Temperature:              0.2,
		FrequencyPenalty:         .2,
		ShowTokens:               true,
		ToolProtocol:             "json",
		SystemInstruction:        DefaultSystemInstruction,
		ShowThinking:             false,
		MarkdownRender:           true,
		CollapseResults:          false,
		HistoryPageSize:          500,
		Logo:                     true,
		LogoBrightness:           55,
		Theme:                    "plain",
		DirectCommands:           false,
		SkillsDir:                resolveDefaultSkillsDir(),
		MCPServers:               make(map[string]MCPServerConfig),
		MaxReasoningSteps:        defaultMaxReasoningSteps,
		MaxCompletionTokens:      defaultMaxCompletionTokens,
		ContextWindowLimit:       defaultContextWindowLimit,
		AutoAdaptContext:         false,
		MinContextWindow:         defaultMinContextWindow,
		CompressionThreshold:     0.85,
		ReasoningEffort:          "off",
		StreamWrites:             true,
		SyntaxTheme:              "monokai",
		Providers:                make(map[string]ProviderConfig),
		CompactPrompt:            true,
		MaxPasteLines:            80,
		MaxPasteChars:            8000,
		Timeout:                  120,
		MaxToolOutputBytes:       limits.DefaultRetainedOutputBytes,
		RepeatReminderThresholds: []int{3, 5},
		RepeatGuardLimit:         10,
		PersistentBash:           true,
		AtomicWrites:             true,
		AskUserMode:              "interactive",
		RecapInterval:            5,
		Recap:                    false,
		Limits:                   limits.Defaults(),
	}
}

func (cfg *Config) defaultProviderProfile(name string) ProviderConfig {
	return ProviderConfig{
		Name:     name,
		Endpoint: cfg.Endpoint,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	}
}

func (cfg *Config) applyDefaults() {
	if cfg.LogoBrightness <= 0 {
		cfg.LogoBrightness = 55
	}
	if cfg.LogoBrightness > 100 {
		cfg.LogoBrightness = 100
	}
	if cfg.PromptMaxLines <= 0 {
		cfg.PromptMaxLines = 8
	}
	if len(cfg.SystemInstruction) > 5000 && strings.Contains(cfg.SystemInstruction, "Principal Software Architect") {
		cfg.SystemInstruction = DefaultSystemInstruction
	}
	if cfg.APIKey == "" {
		if os.Getenv("OPENAI_API_KEY") != "" {
			cfg.APIKey = "env:OPENAI_API_KEY"
		}
	}
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = resolveDefaultSkillsDir()
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]MCPServerConfig)
	}
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.MaxReasoningSteps == 0 {
		cfg.MaxReasoningSteps = defaultMaxReasoningSteps
	}
	if cfg.ContextWindowLimit == 0 {
		cfg.ContextWindowLimit = defaultContextWindowLimit
	}
	if cfg.MinContextWindow <= 0 {
		cfg.MinContextWindow = defaultMinContextWindow
	}
	if cfg.CompressionThreshold == 0.0 {
		cfg.CompressionThreshold = 0.85
	}
	if cfg.MaxPasteLines <= 0 {
		cfg.MaxPasteLines = 80
	}
	if cfg.RecapInterval == 0 {
		cfg.RecapInterval = 5
	}
	if cfg.MaxPasteChars <= 0 {
		cfg.MaxPasteChars = 8000
	}
	if cfg.ReasoningEffort == "" {
		cfg.ReasoningEffort = "off"
	}
	if cfg.SyntaxTheme == "" {
		cfg.SyntaxTheme = "auto"
	}
	if cfg.MaxCompletionTokens == 0 {
		cfg.MaxCompletionTokens = defaultMaxCompletionTokens
	}
	cfg.Limits = limits.Resolve(cfg.Limits)
	cfg.NormalizeSafety()
}

func (cfg *Config) ensureDefaultProvider() {
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	if cfg.ActiveProvider == "" || cfg.ActiveProvider == "default" {
		if _, exists := cfg.Providers["default"]; !exists {
			cfg.ActiveProvider = "default"
			cfg.Providers["default"] = cfg.defaultProviderProfile("default")
		}
	}
}
