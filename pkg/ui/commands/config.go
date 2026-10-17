package commands

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func handleConfigCommand(
	a *agent.Agent,
	cmdName string,
	parts []string,
	messages *[]db.Message,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	rl := terminalFromReader(kiReader)

	if len(parts) <= 1 {
		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := interactive.RunInteractiveConfig(a.Config, *theme, input, output)
		ui.InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			if newConfig.GuidedMode && !a.Config.GuidedMode {
				newConfig.AutoApprove = false
				newConfig.ApprovalAlwaysAnswer = false
				newConfig.ParallelToolCalls = false
			}
			a.ApplyConfig(newConfig)
			ui.SetCollapseStatus(a.Config.CollapseResults)
			*theme = style.ResolveConfiguredTheme(a.Config.Theme, a.Config.SyntaxTheme)
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}
		ui.RestoreAfterModal(w, a, kiReader, rl)
		return
	}

	if cmdName == "/config" && parts[1] == "show" {
		render.RenderConfig(w, a.Config, *theme)
		return
	}

	startIndex := 1
	if cmdName == "/config" && parts[1] == "set" {
		if len(parts) < 4 {
			fmt.Fprintln(w, "usage: /config [set] <key> <value>")
			return
		}
		startIndex = 2
	} else if len(parts) < 3 {
		if cmdName == "/set" {
			fmt.Fprintln(w, "usage: /set <key> <value>")
		} else {
			fmt.Fprintln(w, "usage: /config <key> <value>")
		}
		return
	}

	key := parts[startIndex]
	val := strings.Join(parts[startIndex+1:], " ")

	switch key {
	case "endpoint", "url":
		a.Config.Endpoint = val
		a.Config.UpdateActiveProvider()
	case "model":
		a.Config.Model = val
		a.Config.UpdateActiveProvider()
	case "api_key", "key":
		a.Config.ApiKey = val
		a.Config.UpdateActiveProvider()
	case "temperature", "temp":
		t, err := strconv.ParseFloat(val, 64)
		if err != nil {
			fmt.Fprintf(w, "Invalid temperature value: %v\n", err)
			return
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
	case "reasoning_effort", "reasoning":
		effort, err := config.ParseReasoningEffort(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid reasoning effort '%s'. Allowed values: off, low, medium, high, max\n", val)
			return
		}
		a.Config.ReasoningEffort = effort
	case "before_tool_hook", "before_hook":
		a.Config.BeforeToolHook = val
	case "after_tool_hook", "after_hook":
		a.Config.AfterToolHook = val
	case "collapse_results", "collapse":
		a.Config.CollapseResults = config.ParseBool(val)
		ui.SetCollapseStatus(a.Config.CollapseResults)
	case "show_tokens", "tokens":
		a.Config.ShowTokens = config.ParseBool(val)
	case "theme":
		a.Config.Theme = val
		*theme = ui.GetConfiguredTheme(a.Config)
	case "syntax_theme", "syntax":
		a.Config.SyntaxTheme = val
		*theme = ui.GetConfiguredTheme(a.Config)
	case "context_window_limit", "context_limit", "context":
		if strings.EqualFold(val, "auto") {
			a.Config.AutoAdaptContext = true
			_ = config.SaveConfig(a.ConfigPath, a.Config)
			fmt.Fprintln(w, "context auto-adaptation enabled.")
			return
		}
		if !parseConfigPositiveInt(w, "context window limit", val, &a.Config.ContextWindowLimit) {
			return
		}
	case "auto_adapt_context", "auto_adapt":
		a.Config.AutoAdaptContext = config.ParseBool(val)
	case "min_context_window", "min_context":
		if !parseConfigPositiveInt(w, "min context window", val, &a.Config.MinContextWindow) {
			return
		}
	case "compression_threshold", "compress_threshold", "compression_ratio":
		f, err := config.ParseCompressionThreshold(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid compression threshold: %v\n", err)
			return
		}
		a.Config.CompressionThreshold = f
	case "compaction_reserve_tokens", "compaction_reserve", "reserve_tokens":
		var reserve int
		if !parseConfigPositiveInt(w, "compaction reserve tokens", val, &reserve) {
			return
		}
		a.Config.Limits.CompactionReserveTokens = reserve
		a.Config.Limits = limits.Resolve(a.Config.Limits)
		limits.Set(a.Config.Limits)
	case "max_completion_tokens", "max_tokens", "output_tokens":
		if !parseConfigPositiveInt(w, "max completion tokens", val, &a.Config.MaxCompletionTokens) {
			return
		}
	case "max_reasoning_steps", "max_steps", "steps":
		if !parseConfigPositiveInt(w, "max reasoning steps", val, &a.Config.MaxReasoningSteps) {
			return
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
			return
		}
	case "max_paste_chars", "paste_chars":
		if !parseConfigPositiveInt(w, "max paste chars", val, &a.Config.MaxPasteChars) {
			return
		}
	case "timeout", "llm_timeout":
		sec, err := strconv.Atoi(val)
		if err != nil || sec < 0 {
			fmt.Fprintf(w, "Invalid timeout value: %v (must be non-negative integer)\n", err)
			return
		}
		a.Config.Timeout = sec
	case "max_tool_output_bytes", "spill_limit":
		if !parseConfigPositiveInt(w, "max tool output bytes", val, &a.Config.MaxToolOutputBytes) {
			return
		}
	case "repeat_guard_limit", "guard_limit":
		if !parseConfigPositiveInt(w, "repeat guard limit", val, &a.Config.RepeatGuardLimit) {
			return
		}
	case "persistent_bash", "persistent_shell":
		a.Config.PersistentBash = config.ParseBool(val)
	case "atomic_writes", "atomic_write":
		a.Config.AtomicWrites = config.ParseBool(val)
	case "recap_interval", "recap":
		n, err := strconv.Atoi(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid recap interval: %v (must be an integer; negative disables)\n", err)
			return
		}
		a.Config.RecapInterval = n
	case "prompts_file", "prompts":
		a.Config.PromptsFile = val
		if err := domaintool.LoadPromptCatalog(val); err != nil {
			fmt.Fprintf(w, "prompt catalog not applied: %v\n", err)
		} else {
			fmt.Fprintf(w, "prompt catalog reloaded from %s\n", val)
		}
	case "disable_recap":
		a.Config.DisableRecap = config.ParseBool(val)
	case "ask_user_mode", "ask_user":
		mode, err := config.ParseAskUserMode(val)
		if err != nil {
			fmt.Fprintf(w, "Invalid ask_user_mode '%s'. Allowed: interactive, always_ask, auto_recommended, disabled\n", val)
			return
		}
		a.Config.AskUserMode = mode
		a.Config.UpdateActiveProvider()
	case "disabled_tools", "disable_tools", "disable_tool":
		a.EnableAllTools()
		if cleaned := config.ParseToolList(val); len(cleaned) > 0 {
			a.DisableTools(cleaned...)
		}
	default:
		fmt.Fprintf(w, "unknown config key: %s\n", key)
		return
	}

	a.ApplyConfig(a.Config)
	_ = config.SaveConfig(a.ConfigPath, a.Config)
	fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
	syncCommandStatus(a, messages, *theme, w, kiReader, calcHistoryTokens, true)
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