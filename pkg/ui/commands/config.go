package commands

import (
	"fmt"
	"io"
	"os"
	"reflect"
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
		if errInteractive != nil {
			ui.RestoreAfterModal(w, a, kiReader, rl)
			fmt.Fprintf(w, "settings menu failed: %v\n", errInteractive)
			return
		}
		if newConfig == nil || reflect.DeepEqual(a.Config, newConfig) {
			ui.RestoreAfterModal(w, a, kiReader, rl)
			return
		}

		previous := *a.Config
		if newConfig.GuidedMode && !previous.GuidedMode {
			newConfig.AutoApprove = false
			newConfig.ApprovalAlwaysAnswer = false
			newConfig.ParallelToolCalls = false
		}
		if newConfig.PromptsFile != previous.PromptsFile {
			if err := domaintool.LoadPromptCatalog(newConfig.PromptsFile); err != nil {
				ui.RestoreAfterModal(w, a, kiReader, rl)
				fmt.Fprintf(w, "prompt catalog not applied: %v\n", err)
				return
			}
		}
		a.ApplyConfig(newConfig)
		limits.Set(a.Config.Limits)
		ui.SetCollapseStatus(a.Config.CollapseResults)
		saved := saveConfigUpdate(a, w)
		if redrawConfigIfNeeded(previous, a, theme, w, kiReader, configUpdateNotice(saved, a.ConfigPath), true) {
			return
		}
		ui.RestoreAfterModal(w, a, kiReader, rl)
		if saved {
			fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
		}
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

	previous := *a.Config
	if (key == "context_window_limit" || key == "context_limit" || key == "context") && strings.EqualFold(val, "auto") {
		a.Config.AutoAdaptContext = true
	} else if !setConfigValue(a, key, val, theme, w) {
		return
	}

	a.ApplyConfig(a.Config)
	saved := saveConfigUpdate(a, w)
	if redrawConfigIfNeeded(previous, a, theme, w, kiReader, configUpdateNotice(saved, a.ConfigPath), false) {
		return
	}
	if saved {
		fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
	}
	syncCommandStatus(a, messages, *theme, w, kiReader, calcHistoryTokens, true)
}

// redrawConfigIfNeeded redraws history only for settings that affect its appearance.
// The active renderer is never modified mid-turn; subsequent turns use the new config.
func redrawConfigIfNeeded(previous config.Config, a *agent.Agent, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader, notice string, force bool) bool {
	*theme = style.ResolveConfiguredTheme(a.Config.Theme, a.Config.SyntaxTheme)
	if kiReader != nil {
		kiReader.Theme = *theme
		if kiReader.MAM != nil {
			kiReader.MAM.SetOutput(w, *theme)
		}
	}
	if kiReader == nil || kiReader.RL == nil || (!force && !configAppearanceChanged(previous, *a.Config)) {
		return false
	}
	ui.RedrawScreenWithNotice(w, a, kiReader, kiReader.RL, notice)
	return true
}

func configAppearanceChanged(before, after config.Config) bool {
	return before.MarkdownRender != after.MarkdownRender ||
		before.Theme != after.Theme ||
		before.SyntaxTheme != after.SyntaxTheme ||
		before.CollapseResults != after.CollapseResults ||
		before.ShowThinking != after.ShowThinking ||
		before.ReasoningEffort != after.ReasoningEffort ||
		before.ShowTokens != after.ShowTokens ||
		before.ShowMicroseconds != after.ShowMicroseconds
}

func configUpdateNotice(saved bool, path string) string {
	if !saved {
		return "config applied in memory, but could not save to " + path
	}
	return "config updated. saved to " + path
}

func configRestartHint(before, after config.Config) string {
	if before.CertFile != after.CertFile || before.KeyFile != after.KeyFile ||
		before.CAFile != after.CAFile || before.SkipVerify != after.SkipVerify {
		return " (restart required to rebuild TLS transport)"
	}
	if before.EnableMCP != after.EnableMCP {
		return " (restart may be needed to reconnect MCP servers)"
	}
	return ""
}

// saveConfigUpdate reports an applied-but-not-persisted setting rather than success.
func saveConfigUpdate(a *agent.Agent, w io.Writer) bool {
	if err := config.SaveConfig(a.ConfigPath, a.Config); err != nil {
		fmt.Fprintf(w, "config applied in memory, but could not save to %s: %v\n", a.ConfigPath, err)
		return false
	}
	return true
}
