package interactive

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

// RunInteractiveToolsConfig launches a full-screen interactive menu for toggling tools.
func RunInteractiveToolsConfig(
	a *agent.Agent,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
) error {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, oldState)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGWINCH)
	defer signal.Stop(sigChan)

	sr := newSessionReader(rlInput)
	defer sr.Close()

	fmt.Fprint(rlOutput, terminal.EnterAlternateScreen)
	fmt.Fprint(rlOutput, "\x1b[?25l")
	defer func() {
		fmt.Fprint(rlOutput, "\x1b[?25h")
		fmt.Fprint(rlOutput, terminal.ExitAlternateScreen)
	}()

	itemsProvider := func() []*settingItem {
		if a == nil || a.Registry == nil {
			return nil
		}
		executors := a.Registry.GetAllExecutors()
		var names []string
		for name := range executors {
			names = append(names, name)
		}
		sort.Strings(names)

		var items []*settingItem
		for _, name := range names {
			toolName := name
			exec := executors[toolName]
			desc := ""
			if exec != nil {
				desc = exec.Definition().Function.Description
			}
			items = append(items, &settingItem{
				id:   toolName,
				name: toolName,
				value: func() string {
					if a.IsToolDisabled(toolName) {
						return "disabled"
					}
					return "enabled"
				},
				description: desc,
				isBool:      true,
				onToggle: func() {
					if a.IsToolDisabled(toolName) {
						a.EnableTools(toolName)
					} else {
						a.DisableTools(toolName)
					}
					if a.ConfigPath != "" && a.Config != nil {
						_ = config.SaveConfig(a.ConfigPath, a.Config)
					}
				},
			})
		}
		return items
	}

	extraRender := func(buf *strings.Builder) {
		borderStyle := style.NewStyle().Foreground(theme.Border)
		hintStyle := style.NewStyle().Foreground(theme.Text).Italic(true)
		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n")
		buf.WriteString("  " + hintStyle.Render("Space/Enter: toggle tool | Esc: return | Type to filter"))
		buf.WriteString("\n")
		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n")
	}

	return runSettingsMenuLoop(sr, rlOutput, theme, "tool permissions & availability", itemsProvider, extraRender)
}
