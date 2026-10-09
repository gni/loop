package interactive

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/chroma/v2/quick"
	"golang.org/x/term"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RunInteractiveConfig(cfg *config.Config, theme style.UITheme, rlInput io.Reader, rlOutput io.Writer) (*config.Config, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return cfg, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
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

	formatBool := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}

	cloned := *cfg

	items := buildConfigSettingItems(&cloned, formatBool)

	extraRender := func(buf *strings.Builder) {
		borderStyle := style.NewStyle().Foreground(theme.Border)
		labelStyle := style.NewStyle().Foreground(theme.Secondary).Bold(true)

		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n")
		buf.WriteString("  " + labelStyle.Render("Preview Theme Palette:"))
		buf.WriteString("\n")

		previewTheme := style.ResolveConfiguredTheme(cloned.Theme, cloned.SyntaxTheme)

		pStyle := style.NewStyle().Foreground(previewTheme.Primary).Bold(true)
		sStyle := style.NewStyle().Foreground(previewTheme.Secondary).Bold(true)
		hStyle := style.NewStyle().Foreground(previewTheme.Highlight).Bold(true)
		sucStyle := style.NewStyle().Foreground(previewTheme.Success)
		errStyle := style.NewStyle().Foreground(previewTheme.Error)
		warnStyle := style.NewStyle().Foreground(previewTheme.Warning)
		mStyle := style.NewStyle().Foreground(previewTheme.Border)

		buf.WriteString(fmt.Sprintf("    %s  %s  %s  %s  %s  %s  %s\n",
			pStyle.Render("Primary"),
			sStyle.Render("Secondary"),
			hStyle.Render("Highlight"),
			sucStyle.Render("Success"),
			errStyle.Render("Error"),
			warnStyle.Render("Warning"),
			mStyle.Render("Muted"),
		))

		sampleCode := "func main() {\n    fmt.Println(\"Hello, loop!\")\n}"
		var codeBuf bytes.Buffer
		synTheme := cloned.SyntaxTheme
		if synTheme == "" {
			synTheme = previewTheme.ChromaStyle
		}
		if synTheme == "" {
			synTheme = "monokai"
		}
		errHighlight := quick.Highlight(&codeBuf, sampleCode, "go", "terminal256", synTheme)
		if errHighlight == nil {
			buf.WriteString("  " + labelStyle.Render("Syntax Highlighting ("+synTheme+"):") + "\n")
			lines := strings.Split(codeBuf.String(), "\n")
			for _, line := range lines {
				if strings.TrimSpace(line) != "" {
					buf.WriteString("    " + line + "\n")
				}
			}
		}
		buf.WriteString("  " + borderStyle.Render("──────────────────────────────────────────────────"))
		buf.WriteString("\n")
	}

	itemsProvider := func() []*settingItem {
		return items
	}

	err = runSettingsMenuLoop(sr, rlOutput, theme, "settings", itemsProvider, extraRender)
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}
