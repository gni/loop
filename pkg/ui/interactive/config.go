package interactive

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/alecthomas/chroma/v3/quick"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RunInteractiveConfig(cfg *config.Config, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (*config.Config, error) {
	copy, err := config.Clone(cfg)
	if err != nil {
		return nil, err
	}
	cloned := *copy
	isTTY, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		sigChan := make(chan os.Signal, 1)
		terminal.NotifyResize(sigChan)
		defer signal.Stop(sigChan)

		sr := NewSessionReader(rlInput)
		defer sr.Close()

		formatBool := func(v bool) string {
			if v {
				return "true"
			}
			return "false"
		}

		items := buildConfigSettingItems(&cloned, formatBool)

		extraRender := func(buf *strings.Builder) {
			borderStyle := style.NewStyle().Foreground(theme.Border)
			labelStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)
			previewTheme := style.ResolveConfiguredTheme(cloned.Theme, cloned.SyntaxTheme)

			synTheme := cloned.SyntaxTheme
			if synTheme == "" || synTheme == "auto" {
				synTheme = previewTheme.ChromaStyle
			}
			if synTheme == "" {
				synTheme = "monokai"
			}

			var codeBuf bytes.Buffer
			sampleCode := `func main() { fmt.Println("Hello, loop!") }`
			_ = quick.Highlight(&codeBuf, sampleCode, "go", "terminal256", synTheme)
			codeLine := strings.TrimSpace(codeBuf.String())

			buf.WriteString("  " + borderStyle.Render("────────────────────────────────────────────────────────────────────────") + "\n")
			buf.WriteString(fmt.Sprintf("  %s %s %s %s %s %s %s   %s %s\n",
				labelStyle.Render("Palette:"),
				style.NewStyle().Foreground(previewTheme.Primary).Bold(true).Render("Primary"),
				style.NewStyle().Foreground(previewTheme.Secondary).Bold(true).Render("Secondary"),
				style.NewStyle().Foreground(previewTheme.Highlight).Bold(true).Render("Highlight"),
				style.NewStyle().Foreground(previewTheme.Success).Render("Success"),
				style.NewStyle().Foreground(previewTheme.Error).Render("Error"),
				style.NewStyle().Foreground(previewTheme.Warning).Render("Warning"),
				labelStyle.Render("Syntax ("+synTheme+"):"),
				codeLine,
			))
		}

		advanced := false
		basic := map[string]bool{"logo": true, "reduced_motion": true, "logo_brightness": true, "model": true, "theme": true, "show_thinking": true, "markdown_render": true, "collapse_results": true, "show_tokens": true, "auto_approve": true, "guided_mode": true, "prompt_max_lines": true, "reasoning_effort": true, "timeout": true}
		toggle := &settingItem{id: "advanced", name: "advanced settings", description: "Switch between everyday and all settings", value: func() string {
			if advanced {
				return "shown"
			}
			return "hidden"
		}, onToggle: func() { advanced = !advanced }}
		return runSettingsMenuLoop(sr, rlOutput, theme, "settings", func() []*settingItem {
			result := []*settingItem{toggle}
			for _, item := range items {
				if advanced || basic[item.id] {
					result = append(result, item)
				}
			}
			return result
		}, extraRender)
	})
	if !isTTY {
		return nil, fmt.Errorf("settings editor requires an interactive terminal")
	}
	if err != nil {
		return nil, err
	}
	cloned.NormalizeSafety()
	return &cloned, nil
}
