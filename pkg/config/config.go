package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loop/pkg/domain/limits"
	"loop/pkg/domain/model"
)

type MCPServerConfig struct {
	Exposure   string            `json:"exposure,omitempty"`
	OAuth      *OAuthConfig      `json:"oauth,omitempty"`
	Type       string            `json:"type,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
	AlwaysLoad bool              `json:"alwaysLoad,omitempty"`
}

// ChatCapabilities are explicit opt-ins for non-universal chat-completions fields.
// An OpenAI-compatible URL does not imply support for any of these extensions.
// With no capabilities set, the request contains only model, messages, tools and stream.
type ChatCapabilities struct {
	Temperature         bool `json:"temperature,omitempty"`
	MaxTokens           bool `json:"max_tokens,omitempty"`
	MaxCompletionTokens bool `json:"max_completion_tokens,omitempty"`
	StreamOptions       bool `json:"stream_options,omitempty"`
	ParallelToolCalls   bool `json:"parallel_tool_calls,omitempty"`
	ReasoningEffort     bool `json:"reasoning_effort,omitempty"`
	ThinkingControls    bool `json:"thinking_controls,omitempty"`
	Penalties           bool `json:"penalties,omitempty"`
}

// Merge retains a feature if it was explicitly enabled in either scope.
func (c ChatCapabilities) Merge(other ChatCapabilities) ChatCapabilities {
	return ChatCapabilities{
		Temperature:         c.Temperature || other.Temperature,
		MaxTokens:           c.MaxTokens || other.MaxTokens,
		MaxCompletionTokens: c.MaxCompletionTokens || other.MaxCompletionTokens,
		StreamOptions:       c.StreamOptions || other.StreamOptions,
		ParallelToolCalls:   c.ParallelToolCalls || other.ParallelToolCalls,
		ReasoningEffort:     c.ReasoningEffort || other.ReasoningEffort,
		ThinkingControls:    c.ThinkingControls || other.ThinkingControls,
		Penalties:           c.Penalties || other.Penalties,
	}
}

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

// NormalizeSafety enforces guided mode and migrates legacy ask_user tool disabling.
func (c *Config) NormalizeSafety() {
	if c == nil {
		return
	}
	for _, name := range c.DisabledTools {
		if isAskUserToolName(name) {
			c.SetAskUserMode("disabled")
			break
		}
	}
	if c.GuidedMode {
		c.AutoApprove = false
		c.ApprovalAlwaysAnswer = false
		c.ParallelToolCalls = false
	}
}

type Config struct {
	PromptMaxLines           int                        `json:"prompt_max_lines,omitempty"`
	Nostop                   NostopConfig               `json:"nostop,omitempty"`
	Sources                  map[string]string          `json:"-"`
	ProjectPath              string                     `json:"-"`
	ProjectBase              *Config                    `json:"-"`
	PluginDirectories        []string                   `json:"plugin_directories,omitempty"`
	ExtensionDirectories     []string                   `json:"extension_directories,omitempty"`
	DisableProjectPlugins    bool                       `json:"disable_project_plugins,omitempty"`
	PromptDirectories        []string                   `json:"prompt_directories,omitempty"`
	SkillDirectories         []string                   `json:"skill_directories,omitempty"`
	NoContextFiles           bool                       `json:"no_context_files,omitempty"`
	ShellTimeout             int                        `json:"shell_timeout,omitempty"`
	ShellScanDefaults        bool                       `json:"shell_scan_defaults"`
	ExtensionTimeout         int                        `json:"extension_timeout,omitempty"`
	Endpoint                 string                     `json:"endpoint,omitempty"`
	ApiKey                   string                     `json:"api_key,omitempty"`
	Model                    string                     `json:"model,omitempty"`
	Temperature              float64                    `json:"temperature"`
	FrequencyPenalty         float64                    `json:"frequency_penalty,omitempty"`
	PresencePenalty          float64                    `json:"presence_penalty,omitempty"`
	SystemInstruction        string                     `json:"system_instruction"`
	AutoApprove              bool                       `json:"auto_approve,omitempty"`
	GuidedMode               bool                       `json:"guided_mode,omitempty"`
	ShowThinking             bool                       `json:"show_thinking"`
	MarkdownRender           bool                       `json:"markdown_render"`
	CollapseResults          bool                       `json:"collapse_results,omitempty"`
	ShowTokens               bool                       `json:"show_tokens"`
	ShowMicroseconds         bool                       `json:"show_microseconds"`
	Theme                    string                     `json:"theme"`
	DirectCommands           bool                       `json:"direct_commands"`
	CertFile                 string                     `json:"cert_file,omitempty"`
	KeyFile                  string                     `json:"key_file,omitempty"`
	CAFile                   string                     `json:"ca_file,omitempty"`
	SkipVerify               bool                       `json:"skip_verify"`
	SkillsDir                string                     `json:"skills_dir"`
	MCPServers               map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	EnableMCP                bool                       `json:"enable_mcp,omitempty"`
	ChatCapabilities         ChatCapabilities           `json:"chat_capabilities,omitempty"`
	MaxReasoningSteps        int                        `json:"max_reasoning_steps"`
	ContextWindowLimit       int                        `json:"context_window_limit"`
	AutoAdaptContext         bool                       `json:"auto_adapt_context"`
	MinContextWindow         int                        `json:"min_context_window,omitempty"`
	CompressionThreshold     float64                    `json:"compression_threshold"`
	ReasoningEffort          string                     `json:"reasoning_effort,omitempty"`
	BeforeToolHook           string                     `json:"before_tool_hook,omitempty"`
	AfterToolHook            string                     `json:"after_tool_hook,omitempty"`
	StreamWrites             bool                       `json:"stream_writes"`
	SyntaxTheme              string                     `json:"syntax_theme,omitempty"`
	Providers                map[string]ProviderConfig  `json:"providers,omitempty"`
	ActiveProvider           string                     `json:"active_provider,omitempty"`
	MaxCompletionTokens      int                        `json:"max_completion_tokens,omitempty"`
	CompactPrompt            bool                       `json:"compact_prompt,omitempty"`
	ToolProtocol             string                     `json:"tool_protocol,omitempty"`
	DisableLocalPlugins      bool                       `json:"disable_local_plugins,omitempty"`
	DisabledTools            []string                   `json:"disabled_tools,omitempty"`
	DebugLogFile             string                     `json:"debug_log_file,omitempty"`
	MaxSubagentDepth         int                        `json:"max_subagent_depth,omitempty"`
	MaxPasteLines            int                        `json:"max_paste_lines,omitempty"`
	MaxPasteChars            int                        `json:"max_paste_chars,omitempty"`
	Timeout                  int                        `json:"timeout,omitempty"`
	MaxToolOutputBytes       int                        `json:"max_tool_output_bytes,omitempty"`
	RepeatReminderThresholds []int                      `json:"repeat_reminder_thresholds,omitempty"`
	RepeatGuardLimit         int                        `json:"repeat_guard_limit,omitempty"`
	PersistentBash           bool                       `json:"persistent_bash"`
	AtomicWrites             bool                       `json:"atomic_writes"`
	DisableObservationGuard  bool                       `json:"disable_file_observation_guard,omitempty"`
	PromptCacheBreakpoints   bool                       `json:"prompt_cache_breakpoints,omitempty"`
	AskUserMode              string                     `json:"ask_user_mode,omitempty"`
	ApprovalAlwaysAnswer     bool                       `json:"approval_always_answer,omitempty"`
	ParallelToolCalls        bool                       `json:"parallel_tool_calls"`
	RecapInterval            int                        `json:"recap_interval,omitempty"`
	DisableRecap             bool                       `json:"disable_recap,omitempty"`
	Limits                   limits.Limits              `json:"limits,omitempty"`
	PromptsFile              string                     `json:"prompts_file,omitempty"`
}

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

func saveMCPFile(dir string, servers map[string]MCPServerConfig) error {
	if servers == nil {
		servers = make(map[string]MCPServerConfig)
	}
	data, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MCP configuration: %w", err)
	}
	return writePrivateConfigFile(filepath.Join(dir, "mcp.json"), data)
}

func saveProvidersFile(dir string, active string, providers map[string]ProviderConfig) error {
	clean := make(map[string]ProviderConfig, len(providers))
	for name, p := range providers {
		cp := p
		cp.Name = ""
		clean[name] = cp
	}
	provFile := ProvidersFile{
		Active:         active,
		ActiveProvider: active,
		Providers:      clean,
	}
	data, err := json.MarshalIndent(provFile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode provider configuration: %w", err)
	}
	return writePrivateConfigFile(filepath.Join(dir, "providers.json"), data)
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
		PromptMaxLines:           8,
		Endpoint:                 endpoint,
		ApiKey:                   apiKey,
		Model:                    model,
		Temperature:              0.2,
		SystemInstruction:        DefaultSystemInstruction,
		ShowThinking:             false,
		MarkdownRender:           true,
		Theme:                    "dark",
		DirectCommands:           false,
		SkillsDir:                resolveDefaultSkillsDir(),
		MCPServers:               make(map[string]MCPServerConfig),
		MaxReasoningSteps:        12,
		MaxCompletionTokens:      4096,
		ContextWindowLimit:       8192,
		AutoAdaptContext:         false,
		MinContextWindow:         8192,
		CompressionThreshold:     0.85,
		ReasoningEffort:          "off",
		StreamWrites:             true,
		SyntaxTheme:              "auto",
		Providers:                make(map[string]ProviderConfig),
		CompactPrompt:            true,
		MaxPasteLines:            80,
		MaxPasteChars:            8000,
		Timeout:                  120,
		MaxToolOutputBytes:       8192,
		RepeatReminderThresholds: []int{3, 5},
		RepeatGuardLimit:         10,
		PersistentBash:           true,
		AtomicWrites:             true,
		AskUserMode:              "interactive",
		RecapInterval:            5,
		DisableRecap:             true,
		Limits:                   limits.Defaults(),
	}
}

func (cfg *Config) defaultProviderProfile(name string) ProviderConfig {
	return ProviderConfig{
		Name:     name,
		Endpoint: cfg.Endpoint,
		ApiKey:   cfg.ApiKey,
		Model:    cfg.Model,
		Timeout:  cfg.Timeout,
	}
}

func (cfg *Config) applyDefaults() {
	if cfg.PromptMaxLines <= 0 {
		cfg.PromptMaxLines = 8
	}
	if len(cfg.SystemInstruction) > 5000 && strings.Contains(cfg.SystemInstruction, "Principal Software Architect") {
		cfg.SystemInstruction = DefaultSystemInstruction
	}
	if cfg.ApiKey == "" {
		if os.Getenv("OPENAI_API_KEY") != "" {
			cfg.ApiKey = "env:OPENAI_API_KEY"
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
		cfg.MaxReasoningSteps = 12
	}
	if cfg.ContextWindowLimit == 0 {
		cfg.ContextWindowLimit = 8192
	}
	if cfg.MinContextWindow <= 0 {
		cfg.MinContextWindow = 8192
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
		cfg.MaxCompletionTokens = 4096
	}
	cfg.Limits = limits.Resolve(cfg.Limits)
	cfg.NormalizeSafety()
}

func populateProviderNames(providers map[string]ProviderConfig) {
	for name, p := range providers {
		if p.Name == "" {
			p.Name = name
			providers[name] = p
		}
	}
}

func LoadConfig(path string) (*Config, error) {
	cfg, _, err := loadConfigWithRaw(path)
	return cfg, err
}

func loadConfigWithRaw(path string) (*Config, []byte, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		conf := DefaultConfig()
		if len(embeddedDefaultConfig) > 0 {
			if err := json.Unmarshal(embeddedDefaultConfig, conf); err != nil {
				conf = DefaultConfig()
			}
		}

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

		// Embedded templates may carry legacy high-budget defaults. New
		// installations start in the conservative local preset; existing
		// user configuration remains untouched on subsequent loads.
		conf.Temperature = 0.2
		conf.MaxReasoningSteps = 12
		conf.MaxCompletionTokens = 4096
		conf.ContextWindowLimit = 8192
		conf.MinContextWindow = 8192
		conf.AutoAdaptContext = false
		conf.CompressionThreshold = 0.85
		conf.ReasoningEffort = "off"
		conf.ShowThinking = false
		conf.ParallelToolCalls = false
		conf.DisableRecap = true
		conf.DirectCommands = false
		conf.DisableLocalPlugins = true
		conf.applyDefaults()
		conf.ensureDefaultProvider()
		conf.SyncActiveProvider()
		if err := SaveConfig(path, conf); err != nil {
			return nil, nil, err
		}
		return conf, nil, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}
	cfg.applyDefaults()

	dir := filepath.Dir(path)
	mcpPath := filepath.Join(dir, "mcp.json")
	if mcpData, err := os.ReadFile(mcpPath); err == nil {
		var mcpMap map[string]MCPServerConfig
		if err := json.Unmarshal(mcpData, &mcpMap); err == nil && mcpMap != nil {
			cfg.MCPServers = mcpMap
		}
	} else if len(cfg.MCPServers) > 0 {
		if err := saveMCPFile(dir, cfg.MCPServers); err != nil {
			return nil, nil, err
		}
	}

	providersPath := filepath.Join(dir, "providers.json")
	if provData, err := os.ReadFile(providersPath); err == nil {
		var provFile ProvidersFile
		if err := json.Unmarshal(provData, &provFile); err == nil && provFile.Providers != nil {
			active := provFile.Active
			if active == "" {
				active = provFile.ActiveProvider
			}
			if active != "" {
				cfg.ActiveProvider = active
			}
			populateProviderNames(provFile.Providers)
			cfg.Providers = provFile.Providers
		} else {
			var provMap map[string]ProviderConfig
			if err := json.Unmarshal(provData, &provMap); err == nil && provMap != nil {
				populateProviderNames(provMap)
				cfg.Providers = provMap
			}
		}
	} else if len(cfg.Providers) > 0 {
		if err := saveProvidersFile(dir, cfg.ActiveProvider, cfg.Providers); err != nil {
			return nil, nil, err
		}
	} else if cfg.Endpoint != "" {
		providerName := cfg.ActiveProvider
		if providerName == "" {
			providerName = "default"
			cfg.ActiveProvider = "default"
		}
		cfg.Providers[providerName] = cfg.defaultProviderProfile(providerName)
		if err := saveProvidersFile(dir, cfg.ActiveProvider, cfg.Providers); err != nil {
			return nil, nil, err
		}
	}

	cfg.SyncActiveProvider()
	cfg.NormalizeSafety()
	if envTimeout := os.Getenv("LOOP_TIMEOUT"); envTimeout != "" {
		if t, err := strconv.Atoi(envTimeout); err == nil && t >= 0 {
			cfg.Timeout = t
		}
	}

	return cfg, data, nil
}

func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if err := cfg.ValidatePrompt(); err != nil {
		return err
	}
	if err := cfg.Nostop.Validate(); err != nil {
		return err
	}
	cfg.NormalizeSafety()
	if cfg.ProjectPath != "" && (path == "" || filepath.Clean(path) == filepath.Clean(cfg.ProjectPath)) {
		return saveProjectConfig(cfg)
	}
	copy, err := Clone(cfg)
	if err != nil {
		return err
	}
	cfg = copy
	cfg.NormalizeSafety()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg.UpdateActiveProvider()

	savedMCP := cfg.MCPServers
	savedProviders := cfg.Providers
	cfg.MCPServers = nil
	cfg.Providers = nil

	if profile, ok := savedProviders[cfg.ActiveProvider]; ok {
		if _, env := parseEnvVarKey(profile.ApiKey); env {
			cfg.ApiKey = profile.ApiKey
		}
	}
	data, err := json.MarshalIndent(cfg, "", "  ")

	cfg.MCPServers = savedMCP
	cfg.Providers = savedProviders

	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	if err := writePrivateConfigFile(path, data); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	if cfg.MCPServers == nil {
		cfg.MCPServers = make(map[string]MCPServerConfig)
	}
	if err := saveMCPFile(dir, cfg.MCPServers); err != nil {
		return err
	}

	if cfg.Providers == nil {
		cfg.Providers = make(map[string]ProviderConfig)
	}
	cfg.ensureDefaultProvider()
	if err := saveProvidersFile(dir, cfg.ActiveProvider, cfg.Providers); err != nil {
		return err
	}
	return nil
}

func GetTLSConfig(cfg *Config) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: cfg.SkipVerify,
	}

	if cfg.CertFile != "" && cfg.KeyFile != "" {
		_, errCert := os.Stat(cfg.CertFile)
		_, errKey := os.Stat(cfg.KeyFile)
		if errCert == nil && errKey == nil {
			cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("failed to load client keypair: %w", err)
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
	}

	if cfg.CAFile != "" {
		if _, err := os.Stat(cfg.CAFile); err == nil {
			caCert, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA certificate: %w", err)
			}
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConfig.RootCAs = caCertPool
		}
	}

	return tlsConfig, nil
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringMapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func cloneOAuthConfig(oauth *OAuthConfig) *OAuthConfig {
	if oauth == nil {
		return nil
	}
	copied := *oauth
	if oauth.Scopes != nil {
		copied.Scopes = append([]string(nil), oauth.Scopes...)
	}
	return &copied
}

func CopyMCPServers(servers map[string]MCPServerConfig) map[string]MCPServerConfig {
	if servers == nil {
		return nil
	}
	out := make(map[string]MCPServerConfig, len(servers))
	for name, cfg := range servers {
		copied := cfg
		copied.OAuth = cloneOAuthConfig(cfg.OAuth)
		if cfg.Args != nil {
			copied.Args = append([]string(nil), cfg.Args...)
		}
		copied.Env = cloneStringMap(cfg.Env)
		copied.Headers = cloneStringMap(cfg.Headers)
		out[name] = copied
	}
	return out
}

func CopyProviders(providers map[string]ProviderConfig) map[string]ProviderConfig {
	if providers == nil {
		return nil
	}
	out := make(map[string]ProviderConfig, len(providers))
	for name, p := range providers {
		copied := p
		copied.OAuth = cloneOAuthConfig(p.OAuth)
		copied.Headers = cloneStringMap(p.Headers)
		if p.Models != nil {
			copied.Models = make([]model.Model, len(p.Models))
			for i, m := range p.Models {
				cm := m
				if m.Input != nil {
					cm.Input = append([]string(nil), m.Input...)
				}
				if m.Reasoning != nil {
					cm.Reasoning = append([]string(nil), m.Reasoning...)
				}
				if m.Pricing != nil {
					pr := *m.Pricing
					if m.Pricing.CacheRead != nil {
						cr := *m.Pricing.CacheRead
						pr.CacheRead = &cr
					}
					if m.Pricing.CacheWrite != nil {
						cw := *m.Pricing.CacheWrite
						pr.CacheWrite = &cw
					}
					cm.Pricing = &pr
				}
				copied.Models[i] = cm
			}
		}
		out[name] = copied
	}
	return out
}

func MCPServersEqual(a, b map[string]MCPServerConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for name, cfgA := range a {
		cfgB, exists := b[name]
		if !exists {
			return false
		}
		if cfgA.Type != cfgB.Type || cfgA.Command != cfgB.Command || cfgA.URL != cfgB.URL || cfgA.Disabled != cfgB.Disabled || cfgA.AlwaysLoad != cfgB.AlwaysLoad {
			return false
		}
		if len(cfgA.Args) != len(cfgB.Args) || !stringMapsEqual(cfgA.Env, cfgB.Env) || !stringMapsEqual(cfgA.Headers, cfgB.Headers) {
			return false
		}
		for i := range cfgA.Args {
			if cfgA.Args[i] != cfgB.Args[i] {
				return false
			}
		}
	}
	return true
}

func ParseHeaderArgs(args []string) map[string]string {
	headers := make(map[string]string)
	for _, arg := range args {
		parts := strings.SplitN(arg, ":", 2)
		if len(parts) != 2 {
			parts = strings.SplitN(arg, "=", 2)
		}
		if len(parts) == 2 {
			headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return headers
}

func ParseBool(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

func ParseReasoningEffort(val string) (string, error) {
	switch v := strings.ToLower(strings.TrimSpace(val)); v {
	case "off", "none", "false", "0":
		return "off", nil
	case "low", "medium", "high", "max":
		return v, nil
	default:
		return "", fmt.Errorf("invalid reasoning effort '%s'. Allowed: off, low, medium, high, max", val)
	}
}

func ParseAskUserMode(val string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(val)); mode {
	case "interactive", "enabled", "enable", "on", "true", "1":
		return "interactive", nil
	case "disabled", "disable", "off", "false", "0":
		return "disabled", nil
	case "always_ask", "auto_recommended":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ask_user_mode '%s'. Allowed: interactive (on), always_ask, auto_recommended, disabled (off)", val)
	}
}

func ParseCompressionThreshold(val string) (float64, error) {
	v := strings.TrimSpace(strings.TrimSuffix(val, "%"))
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("must be a positive number (e.g. 0.90 or 90%%)")
	}
	if f > 1.0 {
		f /= 100.0
	}
	if f > 0.98 {
		f = 0.98
	}
	return f, nil
}

func ParseToolList(val string) []string {
	trimmed := strings.TrimSpace(val)
	switch strings.ToLower(trimmed) {
	case "", "none", "clear", "reset", "empty":
		return nil
	}
	var result []string
	for _, p := range strings.Split(trimmed, ",") {
		if t := strings.TrimSpace(p); t != "" {
			result = append(result, t)
		}
	}
	return result
}

func WritePrivateFile(path string, data []byte) error { return writePrivateConfigFile(path, data) }

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
