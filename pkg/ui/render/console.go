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
	DrawStaticPromptSeparatorWithAuto(w, showThinking, reasoningEffort, false, false, theme, width, height, pasteLinesOffset, gitBranch...)
}

func DrawStaticPromptSeparatorWithAuto(w io.Writer, showThinking bool, reasoningEffort string, showAutoBadge, autoApprove bool, theme style.UITheme, width, height, pasteLinesOffset int, gitBranch ...string) {
	_ = DrawStaticPromptSeparatorWithHover(w, showThinking, reasoningEffort, showAutoBadge, autoApprove, false, HoverNone, theme, width, height, pasteLinesOffset, gitBranch...)
}

func DrawStaticPromptSeparatorWithHover(w io.Writer, showThinking bool, reasoningEffort string, showAutoBadge, autoApprove, commandsOpen bool, hovered HoverTarget, theme style.UITheme, width, height, pasteLinesOffset int, gitBranch ...string) []Hitbox {
	if w == nil || height < 5 {
		return nil
	}
	sepRow := height - 3 - max(0, pasteLinesOffset)
	if sepRow < 1 {
		return nil
	}
	line, hitboxes := formatPromptSeparatorLayout(showThinking, reasoningEffort, showAutoBadge, autoApprove, true, commandsOpen, hovered, sepRow, theme, width, gitBranch...)
	var buf bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		buf.WriteString(terminal.BeginSyncOutput)
	}
	fmt.Fprintf(&buf, "\x1b7\x1b[%d;1H", sepRow)
	buf.WriteString(line)
	buf.WriteString(terminal.ResetAttributes)
	buf.WriteString("\x1b[K\x1b8")
	if sync {
		buf.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(buf.Bytes())
	return hitboxes
}

func DrawStatusBar(w io.Writer, theme style.UITheme, width, height, scrollBottom int, indicator, newStatusBarText string) {
	_ = DrawStatusBarWithHover(w, theme, width, height, scrollBottom, indicator, false, false, newStatusBarText)
}

func DrawStatusBarWithHover(w io.Writer, theme style.UITheme, width, height, scrollBottom int, indicator string, collapsed, hovered bool, newStatusBarText string) *Hitbox {
	if w == nil || height < 4 {
		return nil
	}
	sepLine, hb := FormatStatusBarSeparatorLayout(width, height-1, indicator, collapsed, hovered, theme)
	var buf bytes.Buffer
	sync := !inSyncFrame(w)
	if sync {
		buf.WriteString(terminal.BeginSyncOutput)
	}
	buf.WriteString("\x1b7")
	fmt.Fprintf(&buf, "\x1b[1;%dr", max(1, scrollBottom))
	fmt.Fprintf(&buf, "\x1b[%d;1H%s%s\x1b[K", height-1, sepLine, terminal.ResetAttributes)
	fmt.Fprintf(&buf, "\x1b[%d;1H%s%s\x1b[K", height, newStatusBarText, terminal.ResetAttributes)
	buf.WriteString("\x1b8")
	if sync {
		buf.WriteString(terminal.EndSyncOutput)
	}
	_, _ = w.Write(buf.Bytes())
	return hb
}
