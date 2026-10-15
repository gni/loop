package interactive

import (
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui/style"
)

func RunInteractiveToolsConfig(a *agent.Agent, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) error {
	_, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGWINCH)
		defer signal.Stop(sigChan)

		sr := NewSessionReader(rlInput)
		defer sr.Close()

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

			items := make([]*settingItem, 0, len(names))
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
			borderLine := "  " + style.NewStyle().Foreground(theme.Border).Render("──────────────────────────────────────────────────") + "\n"
			hintLine := "  " + style.NewStyle().Foreground(theme.Text).Italic(true).Render("Space/Enter: toggle tool | Esc: return | Type to filter") + "\n"
			buf.WriteString(borderLine + hintLine + borderLine)
		}

		return runSettingsMenuLoop(sr, rlOutput, theme, "tool permissions & availability", itemsProvider, extraRender)
	})
	return err
}