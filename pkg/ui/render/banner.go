package render

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/ui/style"
)

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
	var dirs []string
	home, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(home, ".loop", "extensions"))
	}
	dirs = append(dirs, filepath.Join(a.GetWorkspaceRoot(), "extensions"))

	seen := make(map[string]bool)
	for _, dir := range dirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			info, err := os.Stat(path)
			if err == nil && info.Mode()&0111 != 0 {
				base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				cmdName := "/" + strings.ToLower(base)
				if !seen[cmdName] {
					seen[cmdName] = true
					extensionsCount++
				}
			}
		}
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
