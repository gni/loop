package render

import (
	"bytes"
	"fmt"
	"io"

	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func DrawStaticStatsLine(w io.Writer, theme style.UITheme, spinnerFrame, textToDraw string, height, pasteLinesOffset int) {
	if w == nil || height < 6 {
		return
	}
	statsRow := height - 4 - max(0, pasteLinesOffset)
	if statsRow < 1 {
		return
	}
	var buf bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		buf.WriteString(terminal.BeginSyncOutput)
	}
	buf.WriteString("\x1b7")
	for row := style.ConsoleScrollBottom(height, max(0, pasteLinesOffset)) + 1; row < statsRow; row++ {
		fmt.Fprintf(&buf, "\x1b[%d;1H\x1b[2K", row)
	}
	fmt.Fprintf(&buf, "\x1b[%d;1H", statsRow)

	if content := FormatStaticStatsContent(spinnerFrame, textToDraw, theme); content != "" {
		buf.WriteString(content)
		buf.WriteString(terminal.ResetAttributes)
	}
	buf.WriteString("\x1b[K")
	buf.WriteString("\x1b8")
	if sync {
		buf.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(buf.Bytes())
}

func DrawStaticPromptSeparator(w io.Writer, showThinking bool, reasoningEffort string, theme style.UITheme, width, height, pasteLinesOffset int, gitBranch ...string) {
	if w == nil || height < 5 {
		return
	}
	sepRow := height - 3 - max(0, pasteLinesOffset)
	if sepRow < 1 {
		return
	}
	var buf bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		buf.WriteString(terminal.BeginSyncOutput)
	}
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H", sepRow)
	buf.WriteString(formatPromptSeparatorLine(showThinking, reasoningEffort, theme, width, gitBranch...))
	buf.WriteString(terminal.ResetAttributes)
	buf.WriteString("\x1b[K\x1b8")
	if sync {
		buf.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(buf.Bytes())
}

func DrawStatusBar(w io.Writer, theme style.UITheme, width, height, scrollBottom int, indicator, newStatusBarText string) {
	if w == nil || height < 4 {
		return
	}
	var buf bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		buf.WriteString(terminal.BeginSyncOutput)
	}
	buf.WriteString("\x1b7")
	fmt.Fprintf(&buf, "\x1b[1;%dr", max(1, scrollBottom))
	fmt.Fprintf(&buf, "\x1b[%d;1H%s%s\x1b[K", height-1, FormatStatusBarSeparator(width, indicator, theme), terminal.ResetAttributes)
	fmt.Fprintf(&buf, "\x1b[%d;1H%s%s\x1b[K", height, newStatusBarText, terminal.ResetAttributes)
	buf.WriteString("\x1b8")
	if sync {
		buf.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(buf.Bytes())
}
