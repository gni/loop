package render

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/ui/style"
)

type ExtensionInfo struct {
	Name     string
	Path     string
	Location string
}

var ExtensionCounter func(workspaceRoot string) int

func PrintBanner(w io.Writer, a *agent.Agent, theme style.UITheme) {
	if a == nil || a.Config == nil {
		return
	}
	cfg := a.Config

	iconStyle := style.NewStyle().
		Foreground(theme.Secondary).
		MarginLeft(2)

	infoStyle := style.NewStyle().
		Foreground(theme.Primary).
		Bold(true).
		MarginLeft(4)

	icon := `
  /\___/\
 (  o.o  )
 (  =^=  )
  \_____/`

	pluginsCount := 0
	if a.Registry != nil {
		for name := range a.Registry.GetAllExecutors() {
			if strings.HasPrefix(name, "plugin__") {
				pluginsCount++
			}
		}
	}

	extensionsCount := 0
	if ExtensionCounter != nil {
		extensionsCount = ExtensionCounter(a.GetWorkspaceRoot())
	}

	tagStr := ""
	if pluginsCount > 0 || extensionsCount > 0 {
		tagStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)
		tagStr = "  " + tagStyle.Render(fmt.Sprintf("[⊞ %d, ⌁ %d]", pluginsCount, extensionsCount))
	}

	info := fmt.Sprintf("\n\nloop v1.0.0%s\nendpoint: %s\nmodel:    %s", tagStr, cfg.Endpoint, cfg.Model)

	joined := style.JoinHorizontal(
		style.Center,
		iconStyle.Render(icon),
		infoStyle.Render(info),
	)

	fmt.Fprintln(w, joined)
	fmt.Fprintln(w)
}