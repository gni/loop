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
)

type MCPServerConfig struct {
	Type       string            `json:"type,omitempty"`
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	URL        string            `json:"url,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	Disabled   bool              `json:"disabled,omitempty"`
	AlwaysLoad bool              `json:"alwaysLoad,omitempty"`
}

type Config struct {
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
	CollapseResults          bool                       `json:"collapse_results,omitempty"`
	ShowTokens               bool                       `json:"show_tokens"`
	Theme                    string                     `json:"theme"`
	DirectCommands           bool                       `json:"direct_commands"`
	CertFile                 string                     `json:"cert_file,omitempty"`
	KeyFile                  string                     `json:"key_file,omitempty"`
	CAFile                   string                     `json:"ca_file,omitempty"`
	SkipVerify               bool                       `json:"skip_verify"`
	SkillsDir                string                     `json:"skills_dir"`
	MCPServers               map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
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
	apiKey := os.Getenv("OPENAI_API_KEY")
	endpoint := os.Getenv("OPENAI_API_BASE")
	if endpoint == "" {
		endpoint = "http://localhost:8080"
	}

	model := "llama-3-instruct"
	if apiKey != "" && os.Getenv("OPENAI_API_BASE") == "" {
		endpoint = "http://openai-compatible.local"
		model = "gpt-4-turbo"
	}

	return &Config{
		Endpoint:                 endpoint,
		ApiKey:                   apiKey,
		Model:                    model,
		Temperature:              0.7,
		SystemInstruction:        DefaultSystemInstruction,
		ShowThinking:             true,
		Theme:                    "dark",
		DirectCommands:           true,
		SkillsDir:                resolveDefaultSkillsDir(),
		MCPServers:               make(map[string]MCPServerConfig),
		MaxReasoningSteps:        30,
		MaxCompletionTokens:      16384,
		ContextWindowLimit:       128000,
		AutoAdaptContext:         true,
		MinContextWindow:         32768,
		CompressionThreshold:     0.95,
		ReasoningEffort:          "low",
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
	if len(cfg.SystemInstruction) > 5000 && strings.Contains(cfg.SystemInstruction, "Principal Software Architect") {
		cfg.SystemInstruction = DefaultSystemInstruction
	}
	if cfg.ApiKey == "" {
		cfg.ApiKey = os.Getenv("OPENAI_API_KEY")
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
		cfg.MaxReasoningSteps = 30
	}
	if cfg.ContextWindowLimit == 0 {
		cfg.ContextWindowLimit = 128000
	}
	if cfg.MinContextWindow <= 0 {
		cfg.MinContextWindow = 32768
	}
	if cfg.CompressionThreshold == 0.0 {
		cfg.CompressionThreshold = 0.95
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
		cfg.ReasoningEffort = "low"
	}
	if cfg.SyntaxTheme == "" {
		cfg.SyntaxTheme = "auto"
	}
	if cfg.MaxCompletionTokens == 0 {
		cfg.MaxCompletionTokens = 16384
	}
	cfg.Limits = limits.Resolve(cfg.Limits)
	limits.Set(cfg.Limits)
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

		conf.applyDefaults()
		conf.SyncActiveProvider()
		if err := SaveConfig(path, conf); err != nil {
			return nil, err
		}
		return conf, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := DefaultConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
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
			return nil, err
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
			return nil, err
		}
	} else if cfg.Endpoint != "" {
		providerName := cfg.ActiveProvider
		if providerName == "" {
			providerName = "default"
			cfg.ActiveProvider = "default"
		}
		cfg.Providers[providerName] = cfg.defaultProviderProfile(providerName)
		if err := saveProvidersFile(dir, cfg.ActiveProvider, cfg.Providers); err != nil {
			return nil, err
		}
	}

	cfg.SyncActiveProvider()
	if envTimeout := os.Getenv("LOOP_TIMEOUT"); envTimeout != "" {
		if t, err := strconv.Atoi(envTimeout); err == nil && t >= 0 {
			cfg.Timeout = t
		}
	}

	return cfg, nil
}

func SaveConfig(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	cfg.UpdateActiveProvider()

	savedMCP := cfg.MCPServers
	savedProviders := cfg.Providers
	cfg.MCPServers = nil
	cfg.Providers = nil

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
	if cfg.ActiveProvider == "" || cfg.ActiveProvider == "default" {
		if _, ok := cfg.Providers["default"]; !ok {
			cfg.ActiveProvider = "default"
			cfg.Providers["default"] = cfg.defaultProviderProfile("default")
		}
	}
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

func CopyMCPServers(servers map[string]MCPServerConfig) map[string]MCPServerConfig {
	if servers == nil {
		return nil
	}
	out := make(map[string]MCPServerConfig, len(servers))
	for name, cfg := range servers {
		copied := cfg
		if cfg.Args != nil {
			copied.Args = append([]string(nil), cfg.Args...)
		}
		copied.Env = cloneStringMap(cfg.Env)
		copied.Headers = cloneStringMap(cfg.Headers)
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
	case "interactive", "always_ask", "auto_recommended", "disabled":
		return mode, nil
	default:
		return "", fmt.Errorf("invalid ask_user_mode '%s'. Allowed: interactive, always_ask, auto_recommended, disabled", val)
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
