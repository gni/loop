package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	itheme "loop/pkg/ui/interactive/theme"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleThemeCommand(
	a *agent.Agent,
	parts []string,
	messages *[]db.Message,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
) {
	if a == nil {
		return
	}
	live := a.LiveSettings()
	current := style.CanonicalThemeName(live.Theme)
	if current == "" {
		current = "studio"
	}

	printThemeList := func() {
		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true).Underline(true)
		mutedStyle := style.NewStyle().Foreground(theme.TextMuted)
		activeStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
		fmt.Fprintln(w, headerStyle.Render("available themes:"))
		for _, name := range style.GetThemeNames() {
			t := style.GetTheme(name)
			dots := style.NewStyle().Foreground(t.Primary).Render("●") +
				style.NewStyle().Foreground(t.Secondary).Render("●") +
				style.NewStyle().Foreground(t.Highlight).Render("●") +
				style.NewStyle().Foreground(t.Success).Render("●") +
				style.NewStyle().Foreground(t.Warning).Render("●") +
				style.NewStyle().Foreground(t.Error).Render("●")
			nameStyled := style.NewStyle().Foreground(t.Primary).Bold(true).Render(fmt.Sprintf("%-18s", name))
			suffix := ""
			if name == current {
				suffix = " " + activeStyle.Render("[active]")
			}
			fmt.Fprintf(w, "  %s  %s %s%s\n", dots, nameStyled, mutedStyle.Render(style.ThemeDescription(name)), suffix)
		}
		fmt.Fprintln(w, mutedStyle.Render("usage: /theme [name | next | prev | list] · click ◐ or press Ctrl+Y to cycle"))
	}

	applyTheme := func(target string, fromModal bool) {
		if err := a.UpdateLiveSettings(func(next *agent.LiveSettings) {
			next.Theme = target
			next.SyntaxTheme = "auto"
		}); err != nil {
			if fromModal {
				ui.RestoreAfterModal(w, a, kiReader)
			}
			fmt.Fprintf(w, "theme update failed: %v\n", err)
			return
		}
		saved := saveConfigUpdate(a, w)
		updatedLive := a.LiveSettings()
		ui.GetUI().SetActiveThemeName(target)
		*theme = style.ResolveConfiguredTheme(updatedLive.Theme, updatedLive.SyntaxTheme)
		if kiReader != nil {
			kiReader.SetTheme(*theme)
			if kiReader.MAM != nil {
				kiReader.MAM.SetOutput(w, *theme)
			}
		}
		notice := fmt.Sprintf("theme switched to %s.", target)
		if !saved {
			notice = fmt.Sprintf("theme switched to %s (could not save to %s).", target, a.ConfigPath)
		}
		if kiReader != nil {
			ui.RedrawScreenWithNotice(w, a, kiReader, notice)
			syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
			return
		}
		if fromModal {
			ui.RestoreAfterModal(w, a, kiReader)
		}
		fmt.Fprintln(w, notice)
		syncCommandStatus(a, messages, *theme, w, kiReader, calcHistoryTokens, true)
	}

	if len(parts) > 1 {
		arg := strings.ToLower(strings.TrimSpace(parts[1]))
		switch arg {
		case "list", "show":
			printThemeList()
			return
		case "next", "cycle":
			applyTheme(style.NextThemeName(current, 1), false)
			return
		case "prev", "previous", "back":
			applyTheme(style.NextThemeName(current, -1), false)
			return
		default:
			canon := style.CanonicalThemeName(arg)
			if canon == "" {
				fmt.Fprintf(w, "unknown theme %q. available: %s\n", parts[1], strings.Join(style.GetThemeNames(), ", "))
				return
			}
			applyTheme(canon, false)
			return
		}
	}

	if kiReader == nil && !term.IsTerminal(int(os.Stdin.Fd())) {
		printThemeList()
		return
	}

	input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
	defer cleanup()

	ui.ShutdownStatusBar(os.Stderr)
	picked, err := itheme.RunThemePicker(commandContext(a, kiReader), current, "auto", input, output)
	ui.InitStatusBar(os.Stderr)
	if err != nil {
		ui.RestoreAfterModal(w, a, kiReader)
		if !errors.Is(err, interactive.ErrSelectionCancelled) {
			fmt.Fprintf(w, "theme picker failed: %v\n", err)
		}
		return
	}
	if picked == "" || picked == current {
		ui.RestoreAfterModal(w, a, kiReader)
		return
	}
	applyTheme(picked, true)
}
