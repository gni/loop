package commands

import (
	"fmt"
	"io"
	"strconv"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui"
	"loop/pkg/ui/style"
)

// setConfigValue validates a single value and applies its in-memory effects.
// Persistence is deliberately performed once by handleConfigCommand.
func setConfigValue(a *agent.Agent, key, val string, theme *style.UITheme, w io.Writer) bool {
	switch key {
	case "prompt_max_lines":
		if !parseConfigPositiveInt(w, "prompt max lines", val, &a.Config.PromptMaxLines) {
			return false
		}

	case "endpoint", "url":
		a.Config.Endpoint = val
		a.Config.UpdateActiveProvider()
	case "model":
		a.Config.Model = val
		a.Config.UpdateActiveProvider()
	case "api_key", "key":
		a.Config.APIKey = val
		a.Config.UpdateActiveProvider()
	case "temperature", "temp":
		t, err := strconv.ParseFloat(val, 64)
		if err != nil {
			fmt.Fprintf(w, "Invalid temperature value: %v\n", err)
			return false
		}
		a.Config.Temperature = t
	case "auto_approve", "yes", "yolo":
		a.Config.AutoApprove = config.ParseBool(val)
		if a.Config.AutoApprove {
			a.SetGuidedMode(false)
		}
	case "guided_mode", "guided":
		a.SetGuidedMode(config.ParseBool(val))
	case "approval_always_answer", "always_answer":
		a.Config.ApprovalAlwaysAnswer = config.ParseBool(val)
	case "show_thinking", "thinking":
		a.Config.ShowThinking = config.ParseBool(val)
	case "markdown_render", "markdown":
		a.Config.MarkdownRender = config.ParseBool(val)
	case "reasoning_effort", "reasoning":
		effort, err := config.ParseReasoningEffort(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid reasoning effort '%s'. Allowed values: off, low, medium, high, max\n", val)
			return false
		}
		a.Config.ReasoningEffort = effort
	case "before_tool_hook", "before_hook":
		a.Config.BeforeToolHook = val
	case "after_tool_hook", "after_hook":
		a.Config.AfterToolHook = val
	case "collapse_results", "collapse":
		a.Config.CollapseResults = config.ParseBool(val)
		ui.SetCollapseStatus(a.Config.CollapseResults)
	case "history_page_size", "page_size", "history_chunk", "history_lines":
		if !parseConfigPositiveInt(w, "history page size", val, &a.Config.HistoryPageSize) {
			return false
		}
		if h := ui.GetUI().ChatHistory; h != nil {
			h.SetPageSize(a.Config.HistoryPageSize)
		}
	case "show_tokens", "tokens":
		a.Config.ShowTokens = config.ParseBool(val)
	case "show_microseconds", "microseconds":
		a.Config.ShowMicroseconds = config.ParseBool(val)
	case "theme":
		a.Config.Theme = val
		*theme = ui.GetConfiguredTheme(a.Config)
	case "syntax_theme", "syntax":
		a.Config.SyntaxTheme = val
		*theme = ui.GetConfiguredTheme(a.Config)
	case "context_window_limit", "context_limit", "context":
		if !parseConfigPositiveInt(w, "context window limit", val, &a.Config.ContextWindowLimit) {
			return false
		}
	case "auto_adapt_context", "auto_adapt":
		a.Config.AutoAdaptContext = config.ParseBool(val)
	case "min_context_window", "min_context":
		if !parseConfigPositiveInt(w, "min context window", val, &a.Config.MinContextWindow) {
			return false
		}
	case "compression_threshold", "compress_threshold", "compression_ratio":
		f, err := config.ParseCompressionThreshold(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid compression threshold: %v\n", err)
			return false
		}
		a.Config.CompressionThreshold = f
	case "compaction_reserve_tokens", "compaction_reserve", "reserve_tokens":
		var reserve int
		if !parseConfigPositiveInt(w, "compaction reserve tokens", val, &reserve) {
			return false
		}
		a.Config.Limits.CompactionReserveTokens = reserve
		a.Config.Limits = limits.Resolve(a.Config.Limits)
		limits.Set(a.Config.Limits)
	case "max_completion_tokens", "max_tokens", "output_tokens":
		if !parseConfigPositiveInt(w, "max completion tokens", val, &a.Config.MaxCompletionTokens) {
			return false
		}
	case "max_reasoning_steps", "max_steps", "steps":
		if !parseConfigPositiveInt(w, "max reasoning steps", val, &a.Config.MaxReasoningSteps) {
			return false
		}
	case "direct_commands", "direct":
		a.Config.DirectCommands = config.ParseBool(val)
	case "cert_file", "cert":
		a.Config.CertFile = val
	case "key_file", "client_key":
		a.Config.KeyFile = val
	case "ca_file", "ca":
		a.Config.CAFile = val
	case "skip_verify", "skip":
		a.Config.SkipVerify = config.ParseBool(val)
	case "stream_writes", "stream_write", "stream":
		a.Config.StreamWrites = config.ParseBool(val)
	case "debug_log_file", "debug":
		a.Config.DebugLogFile = val
	case "max_paste_lines", "paste_lines", "paste_threshold":
		if !parseConfigPositiveInt(w, "max paste lines", val, &a.Config.MaxPasteLines) {
			return false
		}
	case "max_paste_chars", "paste_chars":
		if !parseConfigPositiveInt(w, "max paste chars", val, &a.Config.MaxPasteChars) {
			return false
		}
	case "timeout", "llm_timeout":
		sec, err := strconv.Atoi(val)
		if err != nil || sec < 0 {
			fmt.Fprintf(w, "Invalid timeout value: %v (must be non-negative integer)\n", err)
			return false
		}
		a.Config.Timeout = sec
	case "max_tool_output_bytes", "spill_limit":
		if !parseConfigPositiveInt(w, "max tool output bytes", val, &a.Config.MaxToolOutputBytes) {
			return false
		}
	case "repeat_guard_limit", "guard_limit":
		if !parseConfigPositiveInt(w, "repeat guard limit", val, &a.Config.RepeatGuardLimit) {
			return false
		}
	case "persistent_bash", "persistent_shell":
		a.Config.PersistentBash = config.ParseBool(val)
	case "shell_scan_defaults":
		a.Config.ShellScanDefaults = config.ParseBool(val)
	case "atomic_writes", "atomic_write":
		a.Config.AtomicWrites = config.ParseBool(val)
	case "disable_file_observation_guard", "file_guard_off":
		a.Config.DisableObservationGuard = config.ParseBool(val)
	case "recap_interval", "recap":
		n, err := strconv.Atoi(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid recap interval: %v (must be an integer; negative disables)\n", err)
			return false
		}
		a.Config.RecapInterval = n
	case "prompts_file", "prompts":
		if err := domaintool.LoadPromptCatalog(val); err != nil {
			fmt.Fprintf(w, "prompt catalog not applied: %v\n", err)
			return false
		}
		a.Config.PromptsFile = val
		fmt.Fprintf(w, "prompt catalog reloaded from %s\n", val)
	case "disable_recap":
		a.Config.DisableRecap = config.ParseBool(val)
	case "ask_user_mode", "ask_user":
		mode, err := config.ParseAskUserMode(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid ask_user_mode '%s'. Allowed: interactive, always_ask, auto_recommended, disabled\n", val)
			return false
		}
		a.Config.SetAskUserMode(mode)
		a.Config.UpdateActiveProvider()
	case "disabled_tools", "disable_tools", "disable_tool":
		a.EnableAllTools()
		if cleaned := config.ParseToolList(val); len(cleaned) > 0 {
			a.DisableTools(cleaned...)
		}
	default:
		fmt.Fprintf(w, "unknown config key: %s\n", key)
		return false
	}

	return true
}

func parseConfigPositiveInt(w io.Writer, name, val string, target *int) bool {
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		fmt.Fprintf(w, "Invalid %s: %v (must be positive integer)\n", name, err)
		return false
	}
	*target = n
	return true
}
