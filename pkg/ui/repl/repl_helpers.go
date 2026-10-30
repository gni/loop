package repl

import (
	"fmt"
	"golang.org/x/term"
	"io"
	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func configureREPLCallbacks(kiReader *interceptor.KeyInterceptorReader, theme *style.UITheme) {
	kiReader.OnClearPromptHint = func() { ui.GetUI().ClearPromptHint() }
	kiReader.OnRedrawScreen = func() { ui.RedrawScreen(kiReader.W, kiReader.Agent, kiReader, kiReader.RL) }
	kiReader.OnDrawStaticControls = func(targetWriter io.Writer) {
		ui.TerminalMu.Lock()
		ui.DrawConsoleStaticControlsLocked(targetWriter, kiReader.Agent, kiReader, kiReader.RL, true)
		ui.TerminalMu.Unlock()
	}
	kiReader.OnRedrawPromptSeparator = func() {
		ui.DrawStaticPromptSeparator(kiReader.W, kiReader.Agent.Config.ShowThinking, kiReader.Agent.Config.ReasoningEffort, *theme)
	}
	kiReader.OnRedrawStatusBar = func() {
		ui.DrawStatusBar(os.Stderr, ui.GetConfiguredTheme(kiReader.Agent.Config))
	}
	kiReader.OnClearCancelFunc = func() {
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
	}

}

func startREPLInputReader(kiReader *interceptor.KeyInterceptorReader) <-chan byte {
	rawChan := make(chan byte, 1000)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := kiReader.R.Read(buf)
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
					close(rawChan)
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			for i := 0; i < n; i++ {
				rawChan <- buf[i]
			}
		}
	}()

	return rawChan
}

func startREPLSystemNotifications(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader) {

	go func() {
		for event := range a.SystemEvents {
			ui.DrawStatusBar(os.Stderr, ui.GetConfiguredTheme(a.Config))
			if event != "" && kiReader.IsAtMainPrompt {
				ui.GetUI().SetPromptHint(fmt.Sprintf("[%s]", event), 3*time.Second, nil)
			}
		}
	}()

}

func watchREPLResizes(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader, rl *term.Terminal, fd int) func() {
	sigChan := make(chan os.Signal, 1)
	resizeDone := make(chan struct{})
	signal.Notify(sigChan, syscall.SIGWINCH)
	go func() {
		var debounceTimer *time.Timer
		defer func() {
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
		}()
		for {
			select {
			case <-resizeDone:
				return
			case <-sigChan:
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
					select {
					case <-resizeDone:
						return
					default:
					}
					ui.GetUI().StateMu.Lock()
					inModal := ui.GetUI().InApprovalPrompt
					idle := ui.GetUI().ActiveCancelFunc == nil
					ui.GetUI().StateMu.Unlock()
					if inModal {
						return
					}
					if w, h, err := term.GetSize(fd); err == nil {
						rl.SetSize(w, h)
						if idle {
							ui.RedrawScreen(os.Stderr, a, kiReader, rl)
						} else {
							ui.HandleResize(os.Stderr, a, kiReader, rl)
							kiReader.RedrawTypeAhead()
						}
					}
				})
			}
		}
	}()
	return func() {
		signal.Stop(sigChan)
		close(resizeDone)
	}

}

func dispatchREPLSlashCommand(a *agent.Agent, line string, messages *[]db.Message, allowedTools []string, theme *style.UITheme, ppWriter *ui.PromptPreservingWriter, currentSessionID *string, rl *term.Terminal, mam *swarm.MultiAgentManager, kiReader *interceptor.KeyInterceptorReader) (string, bool, bool) {
	trimmedLine := strings.TrimSpace(line)
	if strings.HasPrefix(trimmedLine, "/") || commands.IsHelpCommand(trimmedLine) {
		parts := strings.Fields(trimmedLine)
		if len(parts) > 0 {
			if expanded, isTpl := commands.ExpandPromptTemplate(parts[0], strings.TrimSpace(strings.TrimPrefix(trimmedLine, parts[0]))); isTpl {
				line = expanded
				return line, false, false
			}
		}
		ppWriter.BeginScrollEntry()
		handled, quit := commands.HandleSlashCommand(a, line, messages, allowedTools, theme, ppWriter, currentSessionID, rl.History, mam, kiReader)
		kiReader.Drain()
		if handled {
			return line, true, quit
		}
	}

	return line, false, false
}
