package settings

import (
	"fmt"
	"strconv"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

func buildConfigSettingItems(cloned *config.Config) []*interactive.SettingItem {
	askUserOptions := []string{"interactive", "always_ask", "auto_recommended", "disabled"}
	reasoningOptions := []string{"off", "low", "medium", "high", "max"}

	items := []*interactive.SettingItem{
		boolConfigItem("logo", "logo", "Show the logo in an empty chat", cloned),
		boolConfigItem("reduced_motion", "reduced motion", "Keep the logo still", cloned),
		{ID: "logo_brightness", Name: "logo brightness", Value: func() string { return strconv.Itoa(cloned.LogoBrightness) }, Description: "Logo contrast, 10-100", OnEdit: func(value string) error { return cloned.SetValue("logo_brightness", value) }},
		intConfigItem("prompt_max_lines", "prompt max lines", "Maximum visible prompt rows; grows with text and scrolls beyond this limit (default 8)", cloned),
		{
			ID:          "temperature",
			Name:        "temperature",
			Value:       func() string { return fmt.Sprintf("%.2f", cloned.Temperature) },
			Description: "Sampling temperature for response generation (0.0 to 2.0)",
			OnEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				return cloned.SetValue("temperature", newVal)
			},
		},
		boolConfigItem("guided_mode", "guided mode", "Require workspace exploration, todo plan approval, and diff previews before mutating files", cloned),
		boolConfigItem("file_observation_guard", "file observation guard", "Require a prior read before editing or overwriting files", cloned),
		boolConfigItem("shell_scan_defaults", "shell scan defaults", "Apply dependency exclusions to shell scanners; disabled by default for normal shell behavior", cloned),
		boolConfigItem("auto_approve", "auto approve", "Execute tool operations directly without interactive approval", cloned),
		stringListConfigItem("disabled_tools", "disabled tools", "Comma-separated list of disabled tools (e.g. bash,edit)", &cloned.DisabledTools),
		{
			ID:          "ask_user_mode",
			Name:        "ask user mode",
			Value:       func() string { return cloned.AskUserMode },
			Description: "How ask_user prompts are handled (interactive, always_ask, auto_recommended, disabled)",
			Options:     askUserOptions,
			OnEdit: func(newVal string) error {
				mode, err := config.ParseAskUserMode(newVal)
				if err != nil {
					return err
				}
				cloned.SetAskUserMode(mode)
				return nil
			},
			OnToggle: func() {
				cloned.SetAskUserMode(cycleOption(askUserOptions, cloned.AskUserMode))
			},
		},
		boolConfigItem("show_thinking", "show thinking", "Stream LLM reasoning and internal thought tokens directly to console", cloned),
		boolConfigItem("markdown_render", "markdown render", "Render assistant Markdown live and in session history", cloned),
		{
			ID:          "reasoning_effort",
			Name:        "reasoning effort",
			Value:       func() string { return cloned.ReasoningEffort },
			Description: "Thinking/reasoning budget constraint (off, low, medium, high, max)",
			Options:     reasoningOptions,
			OnEdit: func(newVal string) error {
				effort, err := config.ParseReasoningEffort(newVal)
				if err != nil {
					return err
				}
				cloned.ReasoningEffort = effort
				return nil
			},
			OnToggle: func() {
				cloned.ReasoningEffort = cycleOption(reasoningOptions, cloned.ReasoningEffort)
			},
		},
		intConfigItem("context_window_limit", "context window limit", "Maximum prompt context size allocated before compression (tokens)", cloned),
		optionConfigItem("context_profile", "context profile", "Local presets group existing efficiency settings; numeric fields remain editable", []string{"custom", "local-8k", "local-16k", "local-32k", "8k", "16k", "32k", "64k", "128k", "256k", "512k", "1m"}, cloned),
		optionConfigItem("tool_exposure", "tool exposure", "Core tools with discovery, automatic sizing, or the full catalog", []string{"auto", "core", "full"}, cloned),
		optionConfigItem("history_compression", "history compression", "Safely shorten redundant history independently of adaptive context", []string{"safe", "off"}, cloned),
		boolConfigItem("auto_adapt_context", "auto adapt context", "Automatically scale context window dynamically according to prompt size", cloned),
		intConfigItem("min_context_window", "min context window", "Smallest token window allowed during dynamic context scaling (tokens)", cloned),
		{
			ID:          "compression_threshold",
			Name:        "compression threshold",
			Value:       func() string { return fmt.Sprintf("%.2f", cloned.CompressionThreshold) },
			Description: "Context consumption fraction triggering history compression (e.g. 0.80 to 0.95)",
			OnEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				return cloned.SetValue("compression_threshold", newVal)
			},
		},
		intConfigItem("max_completion_tokens", "max completion tokens", "Hard upper ceiling on generated tokens per single LLM turn", cloned),
		intConfigItem("max_reasoning_steps", "max reasoning steps", "Safety ceiling on autonomous tool execution turns before stopping", cloned),
		boolConfigItem("adaptive_steps", "adaptive steps", "Extend work when new evidence appears and reassess stalled loops", cloned),
		boolConfigItem("direct_commands", "direct commands", "Execute non-tool plain text as system shell instructions when recognized", cloned),
		boolConfigItem("compact_prompt", "compact prompt", "Use short builtin schemas and avoid repeating native tool listings", cloned),
		boolConfigItem("stream_writes", "stream writes", "Show file-write previews as content arrives", cloned),
		boolConfigItem("local_plugins", "local plugins", "Load executable plugins and custom commands", cloned),
		boolConfigItem("context_files", "context files", "Read project instructions and memory files", cloned),
		optionConfigItem("memory_policy", "memory policy", "Automatic excerpts, full optional memory, or optional memory off; directives remain intact", []string{"auto", "full", "off"}, cloned),
		boolConfigItem("persistent_bash", "persistent bash", "Keep shell state between commands", cloned),
		boolConfigItem("atomic_writes", "atomic writes", "Replace files only after the new content is ready", cloned),
		boolConfigItem("strict_syntax", "strict syntax", "Reject invalid Go and JSON before saving; off permits incremental edits", cloned),
		boolConfigItem("prompt_cache_breakpoints", "prompt cache breakpoints", "Send cache hints to providers that support them", cloned),
		boolConfigItem("mcp", "MCP", "Permit configured MCP servers to connect and expose tools", cloned),
		boolConfigItem("parallel_tool_calls", "parallel tool calls", "Execute multiple simultaneous model tool invocations concurrently", cloned),
		boolConfigItem("collapse_results", "collapse results", "Visually collapse lengthy tool execution response text into previews", cloned),
		intConfigItem("history_page_size", "history page size", "Number of rendered terminal lines loaded per scroll chunk and kept resident in memory", cloned),
		boolConfigItem("show_tokens", "show tokens", "Render token count diagnostics at the bottom of agent responses", cloned),
		boolConfigItem("show_reasoning_badge", "show reasoning badge", "Show the reasoning effort indicator on the prompt separator line", cloned),
		boolConfigItem("show_collapse_badge", "show collapse badge", "Show the tool output fold indicator on the status separator line", cloned),
		boolConfigItem("show_git_branch", "show git branch", "Show the active Git branch on the prompt separator line", cloned),
		boolConfigItem("show_sticky_question", "show sticky question", "Pin the active user question at the top of the viewport while scrolling history", cloned),
		boolConfigItem("show_auto_badge", "show auto badge", "Show the bold amber loop indicator (♾︎) on the prompt line when auto approve is active", cloned),
		boolConfigItem("hover_tooltips", "hover tooltips", "Expand status indicators on mouse hover and allow click-to-toggle badges", cloned),
		boolConfigItem("show_microseconds", "show microseconds", "Show six fractional digits on elapsed timers", cloned),
		selectConfigItem("theme", "theme", "Active UI color palette (studio, catppuccin-mocha, tokyo-night, everforest, kanagawa, rose-pine, gruvbox, nord, minimal, zenburn, light)", style.GetThemeNames, &cloned.Theme, "theme"),
		selectConfigItem("syntax_theme", "syntax theme", "Code syntax highlighting style ('auto' matches the active UI theme)", style.GetSyntaxThemeNames, &cloned.SyntaxTheme, "syntax theme"),
		intConfigItem("timeout", "timeout", "Client HTTP and upstream completion request timeout threshold in seconds", cloned),
		intConfigItem("max_subagent_depth", "max subagent depth", "Maximum hierarchical nesting depth for subagent tree delegations", cloned),
		intConfigItem("subagent_timeout", "subagent timeout", "Maximum child execution time in seconds, including queueing", cloned),
		intConfigItem("subagent_token_budget", "subagent token budget", "Maximum total inference tokens allocated to each child task", cloned),
		intConfigItem("max_tool_output_bytes", "max tool output bytes", "Spill threshold in bytes before tool outputs are saved to disk scratch files", cloned),
		intConfigItem("repeat_guard_limit", "repeat guard limit", "Loop repetition detection limit preventing cyclical infinite tool invocations", cloned),
		intListConfigItem("repeat_reminder_thresholds", "repeat reminder thresholds", "Comma-separated repetition counts that inject escalating reminder messages (e.g. 3,5,8)", &cloned.RepeatReminderThresholds),
		intConfigItem("recap_interval", "recap interval", "Inject a turn/token recap into agent history every N reasoning steps (negative disables)", cloned),
		boolConfigItem("recap", "recap", "Show periodic task progress recaps", cloned),
		stringConfigItem("prompts_file", "prompts file", "User-editable prompt catalog layered over embedded defaults (empty uses ~/.loop/prompts.json)", &cloned.PromptsFile),
		stringConfigItem("before_tool_hook", "before tool hook", "Shell command script invoked prior to running tool commands", &cloned.BeforeToolHook),
		stringConfigItem("after_tool_hook", "after tool hook", "Shell command script invoked upon successful tool execution", &cloned.AfterToolHook),
		stringConfigItem("debug_log_file", "debug log file", "Destination file path recording diagnostic agent trace payloads", &cloned.DebugLogFile),
		stringConfigItem("cert_file", "cert file", "Client mTLS x509 certificate file path", &cloned.CertFile),
		stringConfigItem("key_file", "key file", "Client mTLS private key file path", &cloned.KeyFile),
		stringConfigItem("ca_file", "ca file", "Custom Certificate Authority bundle file path", &cloned.CAFile),
		boolConfigItem("tls_verify", "TLS verify", "Verify server certificates", cloned),
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.ID] = true
	}
	// Include every scalar setting so adding a config field cannot leave it
	// silently missing from the editor. Structured settings use JSON via /config.
	for _, key := range config.SettingKeys() {
		if seen[key] || key == "api_key" || key == "system_instruction" {
			continue
		}
		value, _ := cloned.SettingValue(key)
		if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") || value == "null" {
			continue
		}
		items = append(items, &interactive.SettingItem{
			ID: key, Name: strings.ReplaceAll(key, "_", " "),
			Value:       func() string { value, _ := cloned.SettingValue(key); return value },
			Description: "Change " + strings.ReplaceAll(key, "_", " "),
			OnEdit:      func(value string) error { return cloned.SetValue(key, value) },
		})
	}
	return items
}

func matchOption(options []string, val string) (string, bool) {
	for _, opt := range options {
		if strings.EqualFold(opt, val) {
			return opt, true
		}
	}
	return "", false
}

func cycleOption(options []string, current string) string {
	if len(options) == 0 {
		return current
	}
	idx := -1
	for i, opt := range options {
		if strings.EqualFold(opt, current) {
			idx = i
			break
		}
	}
	return options[(idx+1)%len(options)]
}

// Numeric edits use the same validation and default handling as /config.
func intConfigItem(id, name, desc string, cfg *config.Config) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID:          id,
		Name:        name,
		Value:       func() string { value, _ := cfg.SettingValue(id); return value },
		Description: desc,
		OnEdit: func(value string) error {
			if value == "" {
				return nil
			}
			return cfg.SetValue(id, value)
		},
	}
}

func optionConfigItem(id, name, desc string, options []string, cfg *config.Config) *interactive.SettingItem {
	value := func() string { current, _ := cfg.SettingValue(id); return current }
	return &interactive.SettingItem{ID: id, Name: name, Description: desc, Options: options, Value: value, OnEdit: func(text string) error { return cfg.SetValue(id, text) }, OnAction: func() error { return cfg.SetValue(id, cycleOption(options, value())) }}
}

func selectConfigItem(id, name, desc string, getNames func() []string, target *string, label string) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID:          id,
		Name:        name,
		Value:       func() string { return *target },
		Description: desc,
		Options:     getNames(),
		OnEdit: func(newVal string) error {
			if matched, ok := matchOption(getNames(), newVal); ok {
				*target = matched
				return nil
			}
			return fmt.Errorf("invalid %s '%s'", label, newVal)
		},
		OnToggle: func() {
			*target = cycleOption(getNames(), *target)
		},
	}
}

func boolConfigItem(id, name, desc string, cfg *config.Config) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID: id, Name: name, Description: desc, IsBool: true,
		Value:  func() string { value, _ := cfg.SettingValue(id); return value },
		OnEdit: func(value string) error { return cfg.SetValue(id, value) },
	}
}

func intListConfigItem(id, name, desc string, target *[]int) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID:          id,
		Name:        name,
		Value:       func() string { return joinInts(*target) },
		Description: desc,
		OnEdit: func(newVal string) error {
			parsed, err := splitInts(newVal)
			if err != nil {
				return err
			}
			if len(parsed) == 0 {
				return nil
			}
			*target = parsed
			return nil
		},
	}
}

func joinInts(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

func splitInts(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	vals := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid threshold %q", p)
		}
		vals = append(vals, n)
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("thresholds must contain at least one positive integer")
	}
	return vals, nil
}

func stringConfigItem(id, name, desc string, target *string) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID:          id,
		Name:        name,
		Value:       func() string { return *target },
		Description: desc,
		OnEdit: func(newVal string) error {
			*target = newVal
			return nil
		},
	}
}

func stringListConfigItem(id, name, desc string, target *[]string) *interactive.SettingItem {
	return &interactive.SettingItem{
		ID:   id,
		Name: name,
		Value: func() string {
			if target == nil || len(*target) == 0 {
				return "none"
			}
			return strings.Join(*target, ",")
		},
		Description: desc,
		OnEdit: func(newVal string) error {
			*target = config.ParseToolList(newVal)
			return nil
		},
	}
}
