package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/ui"
	"loop/pkg/ui/interactive"
	itheme "loop/pkg/ui/interactive/theme"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func themeDirsForAgent(a *agent.Agent) (userDir string, allDirs []string) {
	configPath := ""
	if a != nil {
		configPath = a.ConfigPath
	}
	userDir = filepath.Join(resources.AgentDirectory(configPath), "themes")
	allDirs = []string{userDir}
	if a != nil {
		if ws := strings.TrimSpace(a.GetWorkspaceRoot()); ws != "" {
			wsDir := filepath.Join(ws, ".loop", "themes")
			if filepath.Clean(wsDir) != filepath.Clean(userDir) {
				allDirs = append(allDirs, wsDir)
			}
		}
	}
	return userDir, allDirs
}

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

	userDir, allDirs := themeDirsForAgent(a)
	_, _ = style.LoadCustomThemes(allDirs...)

	live := a.LiveSettings()
	current := style.CanonicalThemeName(live.Theme)
	if current == "" {
		current = "studio"
	}

	printThemeList := func() {
		headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true).Underline(true)
		sectionStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
		mutedStyle := style.NewStyle().Foreground(theme.TextMuted)
		activeStyle := style.NewStyle().Foreground(theme.Success).Bold(true)
		fmt.Fprintln(w, headerStyle.Render("available themes:"))
		printGroup := func(label, era string) {
			var names []string
			for _, name := range style.GetThemeNames() {
				if style.ThemeEra(name) == era {
					names = append(names, name)
				}
			}
			if len(names) == 0 {
				return
			}
			fmt.Fprintln(w, " "+sectionStyle.Render(label))
			for _, name := range names {
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
		}
		printGroup("custom", "custom")
		printGroup("new generation", "new-gen")
		printGroup("old school", "old-school")
		fmt.Fprintln(w, mutedStyle.Render("usage: /theme [name | create | edit | delete | reload | path | next | prev | list]"))
		fmt.Fprintln(w, mutedStyle.Render("custom themes directory: "+userDir))
	}

	applyThemeWithNotice := func(target, customNotice string, fromModal bool) {
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
		notice := customNotice
		if notice == "" {
			notice = fmt.Sprintf("theme switched to %s.", target)
		}
		if !saved {
			notice = fmt.Sprintf("%s (could not save config to %s)", notice, a.ConfigPath)
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

	applyTheme := func(target string, fromModal bool) {
		applyThemeWithNotice(target, "", fromModal)
	}

	isTTY := kiReader != nil || term.IsTerminal(int(os.Stdin.Fd()))

	openThemeEditor := func(initial style.CustomThemeSpec, isNew bool) {
		input, output, cleanup := interactive.GetInteractiveIO(sessionInputReader(kiReader))
		defer cleanup()

		ui.ShutdownStatusBar(os.Stderr)
		edited, confirmed, err := itheme.RunThemeEditor(commandContext(a, kiReader), initial, isNew, input, output)
		ui.InitStatusBar(os.Stderr)
		if err != nil {
			ui.RestoreAfterModal(w, a, kiReader)
			if !errors.Is(err, interactive.ErrSelectionCancelled) {
				fmt.Fprintf(w, "theme editor failed: %v\n", err)
			}
			return
		}
		if !confirmed {
			ui.RestoreAfterModal(w, a, kiReader)
			return
		}
		savedPath, saveErr := style.SaveCustomTheme(userDir, edited)
		if saveErr != nil {
			ui.RestoreAfterModal(w, a, kiReader)
			fmt.Fprintf(w, "could not save theme: %v\n", saveErr)
			return
		}
		notice := fmt.Sprintf("saved custom theme %s (%s) and switched to it.", edited.Name, savedPath)
		applyThemeWithNotice(edited.Name, notice, true)
	}

	buildEditSpec := func(target string) (style.CustomThemeSpec, bool) {
		spec := style.ThemeSpecFromName(target)
		if style.IsCustomTheme(target) {
			return spec, false
		}
		spec.Name = target + "-custom"
		spec.Description = "custom variant of " + target
		return spec, true
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
		case "path", "dir", "dirs":
			headerStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
			mutedStyle := style.NewStyle().Foreground(theme.TextMuted)
			fmt.Fprintf(w, "%s %s\n", headerStyle.Render("user themes dir:"), userDir)
			if len(allDirs) > 1 {
				fmt.Fprintf(w, "%s %s\n", headerStyle.Render("project themes dir:"), allDirs[1])
			}
			found := 0
			for _, name := range style.GetThemeNames() {
				if p, ok := style.CustomThemePath(name); ok {
					fmt.Fprintf(w, "  %-18s %s\n", name, mutedStyle.Render(p))
					found++
				}
			}
			if found == 0 {
				fmt.Fprintln(w, mutedStyle.Render("no custom themes yet. run /theme create <name> [base] or drop a .json file here:"))
				fmt.Fprintln(w, mutedStyle.Render(`  { "name": "my-ink", "base": "studio", "primary": "#A6C8B8", "highlight": "#E0C28A" }`))
			}
			return
		case "reload":
			count, errs := style.LoadCustomThemes(allDirs...)
			updatedLive := a.LiveSettings()
			active := style.CanonicalThemeName(updatedLive.Theme)
			if active == "" {
				active = "studio"
			}
			*theme = style.ResolveConfiguredTheme(active, updatedLive.SyntaxTheme)
			ui.GetUI().SetActiveThemeName(active)
			if kiReader != nil {
				kiReader.SetTheme(*theme)
				if kiReader.MAM != nil {
					kiReader.MAM.SetOutput(w, *theme)
				}
			}
			notice := fmt.Sprintf("reloaded %d custom theme(s) from %s.", count, userDir)
			if len(errs) > 0 {
				notice = fmt.Sprintf("reloaded %d custom theme(s) with %d warning(s).", count, len(errs))
			}
			if kiReader != nil {
				ui.RedrawScreenWithNotice(w, a, kiReader, notice)
				syncCommandStatus(a, messages, *theme, os.Stderr, kiReader, calcHistoryTokens, false)
			} else {
				fmt.Fprintln(w, notice)
			}
			for _, err := range errs {
				fmt.Fprintf(w, "  warning: %v\n", err)
			}
			return
		case "create", "new", "add":
			candidateName := ""
			baseName := current
			if len(parts) >= 3 {
				candidateName = style.NormalizeCustomThemeName(parts[2])
				if !style.IsValidThemeName(candidateName) {
					fmt.Fprintf(w, "invalid theme name %q (use lowercase letters, digits, '-' or '_').\n", parts[2])
					return
				}
			}
			if len(parts) >= 4 {
				canonBase := style.CanonicalThemeName(parts[3])
				if canonBase == "" {
					fmt.Fprintf(w, "unknown base theme %q. available: %s\n", parts[3], strings.Join(style.GetThemeNames(), ", "))
					return
				}
				baseName = canonBase
			}
			spec := style.ThemeSpecFromName(baseName)
			if candidateName != "" {
				spec.Name = candidateName
			} else {
				spec.Name = baseName + "-custom"
			}
			spec.Description = "custom theme based on " + baseName
			if !isTTY {
				if candidateName == "" {
					fmt.Fprintln(w, "usage: /theme create <name> [base]")
					return
				}
				savedPath, err := style.SaveCustomTheme(userDir, spec)
				if err != nil {
					fmt.Fprintf(w, "could not create theme: %v\n", err)
					return
				}
				applyThemeWithNotice(spec.Name, fmt.Sprintf("created custom theme %s at %s.", spec.Name, savedPath), false)
				return
			}
			openThemeEditor(spec, true)
			return
		case "edit":
			target := current
			if len(parts) >= 3 {
				canon := style.CanonicalThemeName(parts[2])
				if canon == "" {
					fmt.Fprintf(w, "unknown theme %q. available: %s\n", parts[2], strings.Join(style.GetThemeNames(), ", "))
					return
				}
				target = canon
			}
			if !isTTY {
				if p, ok := style.CustomThemePath(target); ok {
					fmt.Fprintf(w, "theme %q file: %s (edit JSON and run /theme reload)\n", target, p)
				} else {
					fmt.Fprintf(w, "%q is a built-in theme. run /theme create <name> %s to customize it.\n", target, target)
				}
				return
			}
			spec, isNew := buildEditSpec(target)
			openThemeEditor(spec, isNew)
			return
		case "delete", "rm", "remove":
			if len(parts) < 3 {
				fmt.Fprintln(w, "usage: /theme delete <custom-theme-name>")
				return
			}
			target := style.NormalizeCustomThemeName(parts[2])
			removedPath, err := style.DeleteCustomTheme(target)
			if err != nil {
				fmt.Fprintf(w, "%v (built-in themes cannot be deleted).\n", err)
				return
			}
			if current == target && style.CanonicalThemeName(target) == "" {
				applyThemeWithNotice("studio", fmt.Sprintf("deleted custom theme %s (%s); switched to studio.", target, removedPath), false)
				return
			}
			fmt.Fprintf(w, "deleted custom theme %s (%s).\n", target, removedPath)
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

	if !isTTY {
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
	switch picked.Action {
	case itheme.PickerCreate:
		base := picked.Base
		if base == "" {
			base = current
		}
		spec := style.ThemeSpecFromName(base)
		spec.Name = base + "-custom"
		spec.Description = "custom theme based on " + base
		openThemeEditor(spec, true)
		return
	case itheme.PickerEdit:
		target := picked.Name
		if target == "" {
			target = current
		}
		spec, isNew := buildEditSpec(target)
		openThemeEditor(spec, isNew)
		return
	default:
		if picked.Name == "" || picked.Name == current {
			ui.RestoreAfterModal(w, a, kiReader)
			return
		}
		applyTheme(picked.Name, true)
	}
}
