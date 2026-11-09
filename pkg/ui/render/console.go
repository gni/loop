package render

import (
	"bytes"
	"fmt"
	"io"

	"loop/pkg/ui/style"
)

func DrawStaticStatsLine(w io.Writer, theme style.UITheme, spinnerFrame, textToDraw string, height, pasteLinesOffset int) {
	var buf bytes.Buffer
	buf.WriteString("\x1b[?2026h\x1b7")
	statsRow := max(1, height-4-pasteLinesOffset)
	for row := style.ConsoleScrollBottom(height, pasteLinesOffset) + 1; row < statsRow; row++ {
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", row)
	}
	fmt.Fprintf(&buf, "\x1b[%d;1H", statsRow)

	content := FormatStaticStatsContent(spinnerFrame, textToDraw, theme)
	if content != "" {
		fmt.Fprint(&buf, content)
	}

	if spinnerFrame == "" && textToDraw == "" {
		fmt.Fprint(&buf, "\x1b[2K")
	} else {
		fmt.Fprint(&buf, "\x1b[K")
	}
	buf.WriteString("\x1b8\x1b[?2026l")
	_, _ = w.Write(buf.Bytes())
}

func DrawStaticPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, width, height, pasteLinesOffset int, gitBranch ...string) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H\x1b[2K", max(1, height-3-pasteLinesOffset))
	fmt.Fprint(&buf, FormatPromptSeparator(showThinking, reasoningEffort, theme, width, gitBranch...))
	fmt.Fprint(&buf, "\x1b8")

	_, _ = w.Write(buf.Bytes())
}

func DrawStatusBar(w io.Writer, theme style.UITheme, width, height, scrollBottom int, indicator, newStatusBarText string) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "\x1b7\x1b[1;%dr\x1b8\x1b[?2026h\x1b7", max(1, scrollBottom))
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s", max(1, height-1), FormatStatusBarSeparator(width, indicator, theme))
	fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K%s", max(1, height), newStatusBarText)
	buf.WriteString("\x1b8\x1b[?2026l")

	_, _ = w.Write(buf.Bytes())
}
