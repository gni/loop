package commands

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
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
	if len(parts) > 1 {
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
		} else if cmdName == "/set" {
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /set <key> <value>")
				return
			}
			startIndex = 1
		}

		if len(parts) <= startIndex {
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
			a.Config.AutoApprove = val == "true" || val == "yes" || val == "1"
		case "approval_always_answer", "always_answer":
			a.Config.ApprovalAlwaysAnswer = parseConfigBool(val)
		case "show_thinking", "thinking":
			enabled := val == "true" || val == "yes" || val == "1" || val == "on"
			a.Config.ShowThinking = enabled
		case "reasoning_effort", "reasoning":
			valLower := strings.ToLower(strings.TrimSpace(val))
			switch valLower {
			case "off", "none", "false", "0":
				a.Config.ReasoningEffort = "off"
			case "low", "medium", "high", "max":
				a.Config.ReasoningEffort = valLower
			default:
				fmt.Fprintf(w, "Invalid reasoning effort '%s'. Allowed values: off, low, medium, high, max\n", val)
				return
			}
		case "before_tool_hook", "before_hook":
			a.Config.BeforeToolHook = val
		case "after_tool_hook", "after_hook":
			a.Config.AfterToolHook = val
		case "collapse_results", "collapse":
			a.Config.CollapseResults = val == "true" || val == "yes" || val == "1"
		case "show_tokens", "tokens":
			a.Config.ShowTokens = val == "true" || val == "yes" || val == "1"
		case "theme":
			a.Config.Theme = val
			*theme = ui.GetConfiguredTheme(a.Config)
		case "syntax_theme", "syntax":
			a.Config.SyntaxTheme = val
			*theme = ui.GetConfiguredTheme(a.Config)
		case "context_window_limit", "context_limit", "context":
			if strings.ToLower(val) == "auto" {
				a.Config.AutoAdaptContext = true
				_ = config.SaveConfig(a.ConfigPath, a.Config)
				fmt.Fprintln(w, "context auto-adaptation enabled.")
				return
			}
			if !parseConfigPositiveInt(w, "context window limit", val, &a.Config.ContextWindowLimit) {
				return
			}
		case "auto_adapt_context", "auto_adapt":
			a.Config.AutoAdaptContext = parseConfigBool(val)
		case "min_context_window", "min_context":
			if !parseConfigPositiveInt(w, "min context window", val, &a.Config.MinContextWindow) {
				return
			}
		case "max_completion_tokens", "max_tokens", "output_tokens":
			if !parseConfigPositiveInt(w, "max completion tokens", val, &a.Config.MaxCompletionTokens) {
				return
			}
		case "max_reasoning_steps", "max_steps", "steps":
			if !parseConfigPositiveInt(w, "max reasoning steps", val, &a.Config.MaxReasoningSteps) {
				return
			}
		case "direct_commands", "direct":
			a.Config.DirectCommands = parseConfigBool(val)
		case "cert_file", "cert":
			a.Config.CertFile = val
		case "key_file", "client_key":
			a.Config.KeyFile = val
		case "ca_file", "ca":
			a.Config.CAFile = val
		case "skip_verify", "skip":
			a.Config.SkipVerify = parseConfigBool(val)
		case "stream_writes", "stream_write", "stream":
			a.Config.StreamWrites = parseConfigBool(val)
		case "debug_log_file", "debug":
			a.Config.DebugLogFile = val
			if a.DebugLogger != nil {
				a.DebugLogger.Close()
			}
			a.DebugLogger = agent.NewDebugLogger(a.WorkspaceRoot, val)
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
			a.Config.PersistentBash = parseConfigBool(val)
		case "atomic_writes", "atomic_write":
			a.Config.AtomicWrites = parseConfigBool(val)
		case "ask_user_mode", "ask_user":
			mode := strings.ToLower(strings.TrimSpace(val))
			if mode != "interactive" && mode != "always_ask" && mode != "auto_recommended" && mode != "disabled" {
				fmt.Fprintf(w, "Invalid ask_user_mode '%s'. Allowed: interactive, always_ask, auto_recommended, disabled\n", val)
				return
			}
			a.Config.AskUserMode = mode
			a.Config.UpdateActiveProvider()
		default:
			fmt.Fprintf(w, "unknown config key: %s\n", key)
			return
		}

		_ = config.SaveConfig(a.ConfigPath, a.Config)
		fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
		pTok, cTok, estimated := calcHistoryTokens()
		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		latestTurnTokens := a.GetLatestAssistantCompletionTokens(msgs)
		effLimit := a.GetEffectiveContextLimit(pTok)
		ui.GetUI().StateMu.Lock()
		ui.GetUI().LastStatusBarText = ""
		ui.GetUI().StateMu.Unlock()
		ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
		var rl *term.Terminal
		if kiReader != nil {
			rl = kiReader.RL
		}
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
		ui.DrawStatusBar(w, *theme)
	} else {
		input, output, cleanup := interactive.GetInteractiveIO(kiReader)
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		newConfig, errInteractive := interactive.RunInteractiveConfig(a.Config, *theme, input, output)
		ui.InitStatusBar(os.Stderr)
		if errInteractive == nil && newConfig != nil {
			a.ApplyConfig(newConfig)
			*theme = style.ResolveConfiguredTheme(a.Config.Theme, a.Config.SyntaxTheme)
			_ = config.SaveConfig(a.ConfigPath, a.Config)
		}

		if kiReader != nil {
			ui.RedrawScreen(w, a, kiReader, kiReader.RL)
		} else {
			ui.RedrawScreen(w, a, nil, nil)
		}
	}
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

func parseConfigBool(val string) bool {
	v := strings.ToLower(strings.TrimSpace(val))
	return v == "true" || v == "1" || v == "yes" || v == "on"
}

