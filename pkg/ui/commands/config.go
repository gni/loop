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
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	isettings "loop/pkg/ui/interactive/settings"
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
	if len(parts) > 1 && cmdName == "/config" && parts[1] == "show" {
		render.RenderConfig(w, a.Config, *theme)
		return
	}
	next, err := a.Configuration()
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}
	modal := len(parts) <= 1
	if modal {
		input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
		defer cleanup()
		ui.ShutdownStatusBar(os.Stderr)
		next, err = isettings.RunInteractiveConfig(a.Config, *theme, input, output)
		ui.InitStatusBar(os.Stderr)
		if err != nil {
			ui.RestoreAfterModal(w, a, kiReader)
			fmt.Fprintf(w, "settings menu failed: %v\n", err)
			return
		}
		if next == nil || reflect.DeepEqual(a.Config, next) {
			ui.RestoreAfterModal(w, a, kiReader)
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
	applyErr := a.UpdateConfig(next)
	if !agent.ConfigApplied(applyErr) {
		if modal {
			ui.RestoreAfterModal(w, a, kiReader)
		}
		fmt.Fprintf(w, "settings update failed: %v\n", applyErr)
		return
	}
	limits.Set(a.Config.Limits)
	ui.SetCollapseStatus(a.LiveSettings().CollapseResults)
	if history := ui.GetUI().GetChatHistory(); history != nil {
		history.SetPageSize(a.Config.HistoryPageSize)
	}
	saved := saveConfigUpdate(a, w)
	notice := configUpdateNotice(saved, a.ConfigPath)
	if applyErr != nil {
		notice += "\n" + applyErr.Error()
	}
	syncConfigTheme(a, theme, w, kiReader)
	if previous.NativeScroll && !a.Config.NativeScroll {
		if history := ui.GetUI().GetChatHistory(); history != nil && messages != nil {
			history.SetNotice(notice)
			_ = history.ResetSession(a.ActiveSessionID(), *messages)
			syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, true)
			return
		}
	}
	if a.Config.NativeScroll {
		// Force-reset mouse tracking and full-screen margins even when native_scroll
		// was already true, so "/config native_scroll true" always unsticks terminals.
		if rawW := agent.UnwrapWriter(w); rawW != nil {
			ui.TerminalMu.Lock()
			_, _ = io.WriteString(rawW, terminal.DisableMouseTracking+terminal.ResetScrollRegion)
			ui.TerminalMu.Unlock()
		}
	}
	if redrawConfigIfNeeded(previous, a, theme, w, kiReader, notice, modal) {
		syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
		return
	}
	if modal {
		ui.RestoreAfterModal(w, a, kiReader)
	}
	if applyErr != nil {
		fmt.Fprintln(w, applyErr)
	}
	if saved {
		fmt.Fprintf(w, "config updated. saved to %s\n", a.ConfigPath)
	}
	syncCommandStatus(a, messages, *theme, w, kiReader, calcHistoryTokens, true)
}

func syncConfigTheme(a *agent.Agent, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader) {
	live := a.LiveSettings()
	ui.GetUI().SetActiveThemeName(live.Theme)
	*theme = style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	if kiReader != nil {
		kiReader.SetTheme(*theme)
		if kiReader.MAM != nil {
			kiReader.MAM.SetOutput(w, *theme)
		}
	}
}

// redrawConfigIfNeeded redraws history only for settings that affect its appearance.
// The active renderer is never modified mid-turn; subsequent turns use the new config.
func redrawConfigIfNeeded(previous config.Config, a *agent.Agent, theme *style.UITheme, w io.Writer, kiReader *interceptor.KeyInterceptorReader, notice string, force bool) bool {
	syncConfigTheme(a, theme, w, kiReader)
	if kiReader == nil || (!force && !configAppearanceChanged(previous, *a.Config)) {
		return false
	}
	ui.RedrawScreenWithNotice(w, a, kiReader, notice)
	return true
}

func configAppearanceChanged(before, after config.Config) bool {
	return before.Logo != after.Logo || before.ReducedMotion != after.ReducedMotion || before.LogoBrightness != after.LogoBrightness || before.MarkdownRender != after.MarkdownRender ||
		before.BashSyntaxColor != after.BashSyntaxColor ||
		before.Theme != after.Theme ||
		before.SyntaxTheme != after.SyntaxTheme ||
		before.CollapseResults != after.CollapseResults ||
		before.ShowThinking != after.ShowThinking ||
		before.ReasoningEffort != after.ReasoningEffort ||
		before.ShowTokens != after.ShowTokens ||
		before.ShowWorkspace != after.ShowWorkspace ||
		before.ShowSession != after.ShowSession ||
		before.AutoApprove != after.AutoApprove ||
		before.ShowReasoningBadge != after.ShowReasoningBadge ||
		before.ShowCollapseBadge != after.ShowCollapseBadge ||
		before.ShowGitBranch != after.ShowGitBranch ||
		before.ShowStickyQuestion != after.ShowStickyQuestion ||
		before.ShowAutoBadge != after.ShowAutoBadge ||
		before.HoverTooltips != after.HoverTooltips ||
		before.NativeScroll != after.NativeScroll ||
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
	if err := a.SaveConfig(); err != nil {
		fmt.Fprintf(w, "config applied in memory, but could not save to %s: %v\n", a.ConfigPath, err)
		return false
	}
	return true
}
