package config

import (
	"loop/pkg/domain/limits"
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
	Enabled    bool              `json:"enabled"`
	AlwaysLoad bool              `json:"always_load"`
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

type Config struct {
	Logo                     bool                       `json:"logo"`
	ReducedMotion            bool                       `json:"reduced_motion"`
	LogoBrightness           int                        `json:"logo_brightness"`
	PromptMaxLines           int                        `json:"prompt_max_lines,omitempty"`
	Nostop                   NostopConfig               `json:"nostop,omitempty"`
	Sources                  map[string]string          `json:"-"`
	ProjectPath              string                     `json:"-"`
	ProjectBase              *Config                    `json:"-"`
	PluginDirectories        []string                   `json:"plugin_directories,omitempty"`
	ExtensionDirectories     []string                   `json:"extension_directories,omitempty"`
	ProjectResources         bool                       `json:"-"`
	PromptDirectories        []string                   `json:"prompt_directories,omitempty"`
	SkillDirectories         []string                   `json:"skill_directories,omitempty"`
	ContextFiles             bool                       `json:"context_files"`
	ShellTimeout             int                        `json:"shell_timeout,omitempty"`
	ShellScanDefaults        bool                       `json:"shell_scan_defaults"`
	ExtensionTimeout         int                        `json:"extension_timeout,omitempty"`
	Endpoint                 string                     `json:"endpoint,omitempty"`
	APIKey                   string                     `json:"api_key,omitempty"`
	Model                    string                     `json:"model,omitempty"`
	Temperature              float64                    `json:"temperature"`
	FrequencyPenalty         float64                    `json:"frequency_penalty,omitempty"`
	PresencePenalty          float64                    `json:"presence_penalty,omitempty"`
	SystemInstruction        string                     `json:"system_instruction"`
	AutoApprove              bool                       `json:"auto_approve"`
	GuidedMode               bool                       `json:"guided_mode"`
	ShowThinking             bool                       `json:"show_thinking"`
	MarkdownRender           bool                       `json:"markdown_render"`
	CollapseResults          bool                       `json:"collapse_results"`
	HistoryPageSize          int                        `json:"history_page_size,omitempty"`
	ShowTokens               bool                       `json:"show_tokens"`
	ShowMicroseconds         bool                       `json:"show_microseconds"`
	Theme                    string                     `json:"theme"`
	DirectCommands           bool                       `json:"direct_commands"`
	CertFile                 string                     `json:"cert_file,omitempty"`
	KeyFile                  string                     `json:"key_file,omitempty"`
	CAFile                   string                     `json:"ca_file,omitempty"`
	TLSVerify                bool                       `json:"tls_verify"`
	SkillsDir                string                     `json:"skills_dir"`
	MCPServers               map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	MCP                      bool                       `json:"mcp"`
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
	CompactPrompt            bool                       `json:"compact_prompt"`
	ToolProtocol             string                     `json:"tool_protocol,omitempty"`
	LocalPlugins             bool                       `json:"local_plugins"`
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
	FileObservationGuard     bool                       `json:"file_observation_guard"`
	PromptCacheBreakpoints   bool                       `json:"prompt_cache_breakpoints"`
	AskUserMode              string                     `json:"ask_user_mode,omitempty"`
	ParallelToolCalls        bool                       `json:"parallel_tool_calls"`
	RecapInterval            int                        `json:"recap_interval,omitempty"`
	Recap                    bool                       `json:"recap"`
	Limits                   limits.Limits              `json:"limits,omitempty"`
	PromptsFile              string                     `json:"prompts_file,omitempty"`
}
