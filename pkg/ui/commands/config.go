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
	if len(parts) > 1 && cmdName == "/config" && parts[1] == "show" {
		render.RenderConfig(w, a.Config, *theme)
		return
	}
	next, err := config.Clone(a.Config)
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	modal := len(parts) <= 1
	if modal {
		input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
		defer cleanup()
		ui.ShutdownStatusBar(os.Stderr)
		next, err = interactive.RunInteractiveConfig(a.Config, *theme, input, output)
		ui.InitStatusBar(os.Stderr)
		if err != nil {
			ui.RestoreAfterModal(w, a, kiReader, rl)
			fmt.Fprintf(w, "settings menu failed: %v\n", err)
			return
		}
		if next == nil || reflect.DeepEqual(a.Config, next) {
			ui.RestoreAfterModal(w, a, kiReader, rl)
			return
		}
	} else {
		index := 1
		if cmdName == "/config" && parts[1] == "set" {
			index = 2
		}
		if len(parts) < index+2 {
			fmt.Fprintln(w, "usage: /config [set] <key> <value>")
			return
		}
		key, value := parts[index], strings.Join(parts[index+1:], " ")
		canonical, _, _ := config.CanonicalSetting(key, value)
		if canonical == "context_window_limit" && strings.EqualFold(value, "auto") {
			next.AutoAdaptContext = true
		} else if !setConfigValue(next, key, value, w) {
			return
		}
	}
	previous := *a.Config
	if err := a.ApplyConfig(next); err != nil {
		if modal {
			ui.RestoreAfterModal(w, a, kiReader, rl)
		}
		fmt.Fprintf(w, "settings update failed: %v\n", err)
		return
	}
	limits.Set(a.Config.Limits)
	ui.SetCollapseStatus(a.Config.CollapseResults)
	if history := ui.GetUI().ChatHistory; history != nil {
		history.SetPageSize(a.Config.HistoryPageSize)
	}
	saved := saveConfigUpdate(a, w)
	if redrawConfigIfNeeded(previous, a, theme, w, kiReader, configUpdateNotice(saved, a.ConfigPath), modal) {
		return
	}
	if modal {
		ui.RestoreAfterModal(w, a, kiReader, rl)
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
	return before.Logo != after.Logo || before.ReducedMotion != after.ReducedMotion || before.LogoBrightness != after.LogoBrightness || before.MarkdownRender != after.MarkdownRender ||
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

// saveConfigUpdate reports an applied-but-not-persisted setting rather than success.
func saveConfigUpdate(a *agent.Agent, w io.Writer) bool {
	if err := config.SaveConfig(a.ConfigPath, a.Config); err != nil {
		fmt.Fprintf(w, "config applied in memory, but could not save to %s: %v\n", a.ConfigPath, err)
		return false
	}
	return true
}
