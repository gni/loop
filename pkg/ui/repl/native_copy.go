package repl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	chathistory "loop/pkg/ui/chat/history"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func resolveNativeCopyContent(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader, chatHistory *chathistory.ChatHistory) (content string, label string) {
	if chatHistory != nil {
		if sel := chatHistory.SelectedPlainText(); strings.TrimSpace(sel) != "" {
			return sel, "selected text"
		}
		nativeScroll := a != nil && a.LiveSettings().NativeScroll
		if !nativeScroll && !chatHistory.HasOlder() {
			if transcript := chatHistory.FullRenderedTranscript(); strings.TrimSpace(style.StripAnsi(transcript)) != "" {
				return transcript, "conversation"
			}
		}
	}

	if rendered := renderSessionTranscriptForCopy(a, kiReader); strings.TrimSpace(style.StripAnsi(rendered)) != "" {
		return rendered, "conversation"
	}

	if chatHistory != nil {
		if transcript := chatHistory.FullRenderedTranscript(); strings.TrimSpace(style.StripAnsi(transcript)) != "" {
			return transcript, "conversation"
		}
	}
	return "", ""
}

func renderSessionTranscriptForCopy(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader) string {
	if a == nil {
		return ""
	}
	var activeMessages []db.Message
	if kiReader != nil && kiReader.Messages != nil {
		a.MessagesMu.RLock()
		activeMessages = message.CloneAll(*kiReader.Messages)
		a.MessagesMu.RUnlock()
	}
	a.CurrentStreamMu.Lock()
	historySnapshot := a.CurrentHistorySnapshot
	a.CurrentStreamMu.Unlock()
	if historySnapshot != nil {
		activeMessages = historySnapshot()
	}
	if kiReader != nil && kiReader.MAM != nil {
		if activeHistory, active := kiReader.MAM.ActiveHistorySnapshot(); active {
			activeMessages = activeHistory
		}
	}
	if len(activeMessages) == 0 {
		return ""
	}
	live := a.LiveSettings()
	activeTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	cfgSnapshot, cfgErr := a.Configuration()
	if cfgErr != nil {
		cfgSnapshot = a.Config
	}
	var buf bytes.Buffer
	ui.PrintSessionHistory(&buf, activeMessages, activeTheme, cfgSnapshot)
	return strings.TrimRight(buf.String(), "\r\n")
}

func normalizeCRLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.ReplaceAll(text, "\n", "\r\n")
}

// waitNativeCopyExit blocks on ch until the user presses Enter, q, Ctrl+X,
// Ctrl+C, Ctrl+D, or a bare Esc key. Multi-byte escape sequences (arrows,
// PageUp/PageDown, focus events) are consumed intact without exiting.
func waitNativeCopyExit(ctx context.Context, ch <-chan byte) {
	if ch == nil {
		return
	}
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	escTimeout := limits.EscapeDisambiguation()
	if escTimeout <= 0 {
		escTimeout = 30 * time.Millisecond
	}

	readWithTimeout := func(d time.Duration) (byte, bool) {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-done:
			return 0, false
		case b, ok := <-ch:
			if !ok {
				return 0, false
			}
			return b, true
		case <-timer.C:
			return 0, false
		}
	}

	for {
		var b byte
		var ok bool
		select {
		case <-done:
			return
		case b, ok = <-ch:
			if !ok {
				return
			}
		}

		switch b {
		case '\r', '\n', 'q', 'Q', 24, 3, 4:
			return
		case 27:
			next, hasFollowup := readWithTimeout(escTimeout)
			if !hasFollowup {
				return
			}
			switch next {
			case '[':
				for range 64 {
					nb, bok := readWithTimeout(escTimeout)
					if !bok || (nb >= 0x40 && nb <= 0x7e) {
						break
					}
				}
			case 'O':
				_, _ = readWithTimeout(escTimeout)
			}
		}
	}
}

// runNativeCopyView temporarily suspends mouse capture and scroll-region
// margins, prints the selected text or full conversation directly onto the
// primary screen with \r\n so it enters the terminal emulator's real native
// scrollback for universal drag-selection and copy, and cleanly restores the
// interactive REPL screen on exit.
func runNativeCopyView(w io.Writer, a *agent.Agent, kiReader *interceptor.KeyInterceptorReader, chatHistory *chathistory.ChatHistory) {
	if w == nil || kiReader == nil {
		return
	}
	if kiReader.ActiveTurn.Load() {
		return
	}
	state := ui.GetUI()
	state.StateMu.Lock()
	if state.InInteractiveMenu || state.InApprovalPrompt || terminal.Default().InAltScreen() {
		state.StateMu.Unlock()
		return
	}
	state.InInteractiveMenu = true
	state.StateMu.Unlock()

	content, label := resolveNativeCopyContent(a, kiReader, chatHistory)
	if content == "" {
		state.StateMu.Lock()
		state.InInteractiveMenu = false
		state.StateMu.Unlock()
		if kiReader.IsAtMainPrompt.Load() {
			state.SetPromptHint("nothing to copy yet", 2*time.Second, func() {
				if kiReader.InputContext != nil && kiReader.InputContext.Err() != nil {
					return
				}
				if kiReader.IsAtMainPrompt.Load() {
					ui.RefreshConsoleAfterPromptCancellation(w, a, kiReader)
				}
			})
			ui.RefreshConsoleAfterPromptCancellation(w, a, kiReader)
		} else if a != nil {
			if uiImpl, ok := a.UI.(*ui.AgentUIImpl); ok && uiImpl.PPWriter != nil {
				fmt.Fprintln(uiImpl.PPWriter, "Nothing to copy yet.")
			}
		}
		return
	}

	var restoreTerm func()
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		if oldState, err := term.MakeRaw(fd); err == nil {
			restoreTerm = func() { _ = term.Restore(fd, oldState) }
		}
	}

	ui.TerminalMu.Lock()
	resumeMouse := terminal.Default().SuspendMouseTracking()
	width, _ := ui.GetTerminalSize()
	var theme style.UITheme
	if a != nil {
		live := a.LiveSettings()
		theme = style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	} else {
		theme = style.GetTheme("studio")
	}
	borderStyle := style.NewStyle().Foreground(theme.Border)
	mutedStyle := style.NewStyle().Foreground(theme.TextMuted)
	accentStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)

	rule := strings.Repeat("─", max(20, min(width-1, 80)))
	var out bytes.Buffer
	out.WriteString("\x1b[r\x1b[2J\x1b[H\x1b[?25h")
	out.WriteString(normalizeCRLF(content))
	out.WriteString(terminal.ResetAttributes)
	out.WriteString("\r\n\r\n")
	out.WriteString(borderStyle.Render(rule))
	out.WriteString("\r\n")
	fmt.Fprintf(&out, "%s %s\r\n",
		accentStyle.Render("Native copy view ("+label+")"),
		mutedStyle.Render("· select, scroll, and copy with your terminal (Ctrl+Shift+C / Cmd+C) · Enter, Esc, or q to return"),
	)
	_, _ = w.Write(out.Bytes())
	ui.TerminalMu.Unlock()

	waitNativeCopyExit(kiReader.InputContext, kiReader.InputChan)

	if restoreTerm != nil {
		restoreTerm()
	}

	ui.TerminalMu.Lock()
	resumeMouse()
	_, _ = io.WriteString(w, "\x1b[r\x1b[2J\x1b[H")
	ui.TerminalMu.Unlock()

	state.StateMu.Lock()
	state.InInteractiveMenu = false
	state.LastH = 0
	state.LastStatusBarText = ""
	state.StateMu.Unlock()

	kiReader.Drain()
	if a != nil {
		ui.RedrawScreen(w, a, kiReader)
	}
}
