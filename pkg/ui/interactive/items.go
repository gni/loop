package interactive

import (
	"fmt"
	"strconv"
	"strings"

	"loop/pkg/config"
	"loop/pkg/ui/style"
)

func buildConfigSettingItems(cloned *config.Config, formatBool func(v bool) string) []*settingItem {
	return []*settingItem{
		{
			id:          "temperature",
			name:        "temperature",
			value:       func() string { return fmt.Sprintf("%.2f", cloned.Temperature) },
			description: "Sampling temperature for response generation (0.0 to 2.0)",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				v, err := strconv.ParseFloat(newVal, 64)
				if err != nil {
					return fmt.Errorf("invalid float")
				}
				if v < 0.0 || v > 2.0 {
					return fmt.Errorf("must be between 0.0 and 2.0")
				}
				cloned.Temperature = v
				return nil
			},
		},
		boolConfigItem("auto_approve", "auto approve", "Execute tool operations directly without interactive approval", &cloned.AutoApprove, formatBool),
		boolConfigItem("approval_always_answer", "approval always answer", "Skip the approval modal and pre-answer 'always' for unattended loop runs", &cloned.ApprovalAlwaysAnswer, formatBool),
		{
			id:          "ask_user_mode",
			name:        "ask user mode",
			value:       func() string { return cloned.AskUserMode },
			description: "How ask_user prompts are handled (interactive, always_ask, auto_recommended, disabled)",
			options:     []string{"interactive", "always_ask", "auto_recommended", "disabled"},
			onEdit: func(newVal string) error {
				v := strings.ToLower(strings.TrimSpace(newVal))
				switch v {
				case "interactive", "always_ask", "auto_recommended", "disabled":
					cloned.AskUserMode = v
					return nil
				default:
					return fmt.Errorf("invalid ask_user_mode '%s'. Allowed: interactive, always_ask, auto_recommended, disabled", newVal)
				}
			},
			onToggle: func() {
				opts := []string{"interactive", "always_ask", "auto_recommended", "disabled"}
				cloned.AskUserMode = cycleOption(opts, cloned.AskUserMode)
			},
		},
		{
			id:          "show_thinking",
			name:        "show thinking",
			value:       func() string { return formatBool(cloned.ShowThinking) },
			description: "Stream LLM reasoning and internal thought tokens directly to console",
			isBool:      true,
			onToggle: func() {
				cloned.ShowThinking = !cloned.ShowThinking
			},
		},
		{
			id:          "reasoning_effort",
			name:        "reasoning effort",
			value:       func() string { return cloned.ReasoningEffort },
			description: "Thinking/reasoning budget constraint (off, low, medium, high, max)",
			options:     []string{"off", "low", "medium", "high", "max"},
			onEdit: func(newVal string) error {
				v := strings.ToLower(strings.TrimSpace(newVal))
				switch v {
				case "off", "none", "false", "0":
					cloned.ReasoningEffort = "off"
					return nil
				case "low", "medium", "high", "max":
					cloned.ReasoningEffort = v
					return nil
				default:
					return fmt.Errorf("invalid reasoning effort '%s'. Allowed: off, low, medium, high, max", newVal)
				}
			},
			onToggle: func() {
				opts := []string{"off", "low", "medium", "high", "max"}
				cloned.ReasoningEffort = cycleOption(opts, cloned.ReasoningEffort)
			},
		},
		intConfigItem("context_window_limit", "context window limit", "Maximum prompt context size allocated before compression (tokens)", &cloned.ContextWindowLimit),
		boolConfigItem("auto_adapt_context", "auto adapt context", "Automatically scale context window dynamically according to prompt size", &cloned.AutoAdaptContext, formatBool),
		intConfigItem("min_context_window", "min context window", "Smallest token window allowed during dynamic context scaling (tokens)", &cloned.MinContextWindow),
		intConfigItem("max_completion_tokens", "max completion tokens", "Hard upper ceiling on generated tokens per single LLM turn", &cloned.MaxCompletionTokens),
		intConfigItem("max_reasoning_steps", "max reasoning steps", "Safety ceiling on autonomous tool execution turns before stopping", &cloned.MaxReasoningSteps),
		boolConfigItem("direct_commands", "direct commands", "Execute non-tool plain text as system shell instructions when recognized", &cloned.DirectCommands, formatBool),
		boolConfigItem("compact_prompt", "compact prompt", "Compress standard system guidelines into dense short instructions", &cloned.CompactPrompt, formatBool),
		boolConfigItem("stream_writes", "stream writes", "Stream tool output and completions smoothly token-by-token", &cloned.StreamWrites, formatBool),
		boolConfigItem("parallel_tool_calls", "parallel tool calls", "Execute multiple simultaneous model tool invocations concurrently", &cloned.ParallelToolCalls, formatBool),
		boolConfigItem("collapse_results", "collapse results", "Visually collapse lengthy tool execution response text into previews", &cloned.CollapseResults, formatBool),
		boolConfigItem("show_tokens", "show tokens", "Render token count diagnostics at the bottom of agent responses", &cloned.ShowTokens, formatBool),
		selectConfigItem("theme", "theme", "Active UI color palette (default, minimal, matrix, amber, monokai)", style.GetThemeNames, &cloned.Theme, "theme"),
		selectConfigItem("syntax_theme", "syntax theme", "Chroma syntax highlighting style for code preview blocks", style.GetSyntaxThemeNames, &cloned.SyntaxTheme, "syntax theme"),
		intConfigItem("timeout", "timeout", "Client HTTP and upstream completion request timeout threshold in seconds", &cloned.Timeout),
		{
			id:          "max_subagent_depth",
			name:        "max subagent depth",
			value:       func() string { return strconv.Itoa(cloned.MaxSubagentDepth) },
			description: "Maximum hierarchical nesting depth for subagent tree delegations",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				n, err := strconv.Atoi(newVal)
				if err != nil || n < 0 {
					return fmt.Errorf("must be a non-negative integer")
				}
				cloned.MaxSubagentDepth = n
				return nil
			},
		},
		intConfigItem("max_tool_output_bytes", "max tool output bytes", "Spill threshold in bytes before tool outputs are saved to disk scratch files", &cloned.MaxToolOutputBytes),
		intConfigItem("repeat_guard_limit", "repeat guard limit", "Loop repetition detection limit preventing cyclical infinite tool invocations", &cloned.RepeatGuardLimit),
		intListConfigItem("repeat_reminder_thresholds", "repeat reminder thresholds", "Comma-separated repetition counts that inject escalating reminder messages (e.g. 3,5,8)", &cloned.RepeatReminderThresholds),
		{
			id:          "recap_interval",
			name:        "recap interval",
			value:       func() string { return strconv.Itoa(cloned.RecapInterval) },
			description: "Inject a turn/token recap into agent history every N reasoning steps (negative disables)",
			onEdit: func(newVal string) error {
				if newVal == "" {
					return nil
				}
				n, err := strconv.Atoi(newVal)
				if err != nil {
					return fmt.Errorf("must be an integer (negative disables)")
				}
				cloned.RecapInterval = n
				return nil
			},
		},
		boolConfigItem("disable_recap", "recap disabled", "Suppress the turn/token recap line entirely, independently of the interval", &cloned.DisableRecap, formatBool),
		stringConfigItem("before_tool_hook", "before tool hook", "Shell command script invoked prior to running tool commands", &cloned.BeforeToolHook),
		stringConfigItem("after_tool_hook", "after tool hook", "Shell command script invoked upon successful tool execution", &cloned.AfterToolHook),
		stringConfigItem("debug_log_file", "debug log file", "Destination file path recording diagnostic agent trace payloads", &cloned.DebugLogFile),
		stringConfigItem("cert_file", "cert file", "Client mTLS x509 certificate file path", &cloned.CertFile),
		stringConfigItem("key_file", "key file", "Client mTLS private key file path", &cloned.KeyFile),
		stringConfigItem("ca_file", "ca file", "Custom Certificate Authority bundle file path", &cloned.CAFile),
		boolConfigItem("skip_verify", "skip verify", "Disable TLS certificate authenticity validation", &cloned.SkipVerify, formatBool),
	}
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
	nextIdx := (idx + 1) % len(options)
	return options[nextIdx]
}

func intConfigItem(id, name, desc string, target *int) *settingItem {
	return &settingItem{
		id:          id,
		name:        name,
		value:       func() string { return strconv.Itoa(*target) },
		description: desc,
		onEdit: func(newVal string) error {
			return parsePositiveInt(newVal, target)
		},
	}
}

func selectConfigItem(id, name, desc string, getNames func() []string, target *string, label string) *settingItem {
	return &settingItem{
		id:          id,
		name:        name,
		value:       func() string { return *target },
		description: desc,
		options:     getNames(),
		onEdit: func(newVal string) error {
			if matched, ok := matchOption(getNames(), newVal); ok {
				*target = matched
				return nil
			}
			return fmt.Errorf("invalid %s '%s'", label, newVal)
		},
		onToggle: func() {
			*target = cycleOption(getNames(), *target)
		},
	}
}

func boolConfigItem(id, name, desc string, target *bool, formatBool func(bool) string) *settingItem {
	return &settingItem{
		id:          id,
		name:        name,
		value:       func() string { return formatBool(*target) },
		description: desc,
		isBool:      true,
		onToggle: func() {
			*target = !*target
		},
	}
}

func intListConfigItem(id, name, desc string, target *[]int) *settingItem {
	return &settingItem{
		id:          id,
		name:        name,
		value:       func() string { return joinInts(*target) },
		description: desc,
		onEdit: func(newVal string) error {
			parsed, err := splitInts(newVal)
			if err != nil {
				return err
			}
			// Empty input is a no-op, consistent with parsePositiveInt: pressing
			// enter without typing must not silently wipe the thresholds.
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

func stringConfigItem(id, name, desc string, target *string) *settingItem {
	return &settingItem{
		id:          id,
		name:        name,
		value:       func() string { return *target },
		description: desc,
		onEdit: func(newVal string) error {
			*target = newVal
			return nil
		},
	}
}
