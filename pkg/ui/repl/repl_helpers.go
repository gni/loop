package repl

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func configureREPLCallbacks(kiReader *interceptor.KeyInterceptorReader, _ *style.UITheme) {
	resolveTheme := func() style.UITheme {
		live := kiReader.Agent.LiveSettings()
		return style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	}

	kiReader.OnQueueChanged = func() {
		ui.GetUI().StateMu.Lock()
		ui.GetUI().State.QueuedPromptsCount = kiReader.QueueLen()
		ui.GetUI().StateMu.Unlock()
		if !ui.GetUI().IsInModal() {
			ui.TerminalMu.Lock()
			if !ui.GetUI().IsInModal() {
				ui.DrawConsoleStaticControlsLocked(kiReader.W, kiReader.Agent, kiReader, true)
			}
			ui.TerminalMu.Unlock()
			return
		}
		ui.DrawStatusBar(kiReader.W, resolveTheme())
	}
	kiReader.OnRedrawTypeAhead = func() {
		if ui.GetUI().IsInModal() {
			return
		}
		ui.TerminalMu.Lock()
		defer ui.TerminalMu.Unlock()
		if ui.GetUI().IsInModal() {
			return
		}
		ui.DrawConsoleStaticControlsLocked(kiReader.W, kiReader.Agent, kiReader, true)
	}
	kiReader.OnClearPromptHint = func() { ui.GetUI().ClearPromptHint() }
	kiReader.OnRedrawScreen = func() { ui.RedrawScreen(kiReader.W, kiReader.Agent, kiReader) }
	kiReader.OnDrawStaticControls = func(targetWriter io.Writer) {
		if ui.GetUI().IsInModal() {
			return
		}
		ui.TerminalMu.Lock()
		defer ui.TerminalMu.Unlock()
		if ui.GetUI().IsInModal() {
			return
		}
		ui.DrawConsoleStaticControlsLocked(targetWriter, kiReader.Agent, kiReader, true)
	}
	kiReader.OnRedrawPromptSeparator = func() {
		live := kiReader.Agent.LiveSettings()
		ui.DrawStaticPromptSeparator(kiReader.W, live.ShowThinking, live.ReasoningEffort, style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme))
	}
	kiReader.OnRedrawStatusBar = func() {
		ui.DrawStatusBar(os.Stderr, resolveTheme())
	}
	kiReader.OnThemeChanged = func(name string, newTheme style.UITheme) {
		ui.GetUI().SetActiveThemeName(name)
		if kiReader.MAM != nil {
			if uiImpl, ok := kiReader.Agent.UI.(*ui.AgentUIImpl); ok && uiImpl.PPWriter != nil {
				kiReader.MAM.SetOutput(uiImpl.PPWriter, newTheme)
			}
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().LastStatusBarText = ""
		ui.GetUI().StateMu.Unlock()
		if !kiReader.ActiveTurn.Load() && kiReader.IsAtMainPrompt.Load() {
			ui.GetUI().SetPromptHint(fmt.Sprintf("theme: %s", name), 2*time.Second, func() {
				if kiReader.InputContext != nil && kiReader.InputContext.Err() != nil {
					return
				}
				if kiReader.IsAtMainPrompt.Load() {
					ui.RefreshConsoleAfterPromptCancellation(os.Stderr, kiReader.Agent, kiReader)
				}
			})
		}
		if kiReader.ActiveTurn.Load() {
			kiReader.PendingThemeRefresh.Store(true)
			live := kiReader.Agent.LiveSettings()
			ui.DrawStaticPromptSeparator(kiReader.W, live.ShowThinking, live.ReasoningEffort, newTheme)
			ui.DrawStatusBar(os.Stderr, newTheme)
			return
		}
		ui.RedrawScreen(kiReader.W, kiReader.Agent, kiReader)
	}
	kiReader.OnClearCancelFunc = func() {
		ui.GetUI().SetActiveCancel(nil)
	}
}

func startREPLInputReader(kiReader *interceptor.KeyInterceptorReader) <-chan byte {
	rawChan := make(chan byte, 1000)
	go func() {
		defer close(rawChan)
		var done <-chan struct{}
		if kiReader.InputContext != nil {
			done = kiReader.InputContext.Done()
		}
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
			}
			n, err := kiReader.R.Read(buf)
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "closed") {
					return
				}
				timer := time.NewTimer(50 * time.Millisecond)
				select {
				case <-done:
					timer.Stop()
					return
				case <-timer.C:
				}
				continue
			}
			for i := range n {
				select {
				case <-done:
					return
				case rawChan <- buf[i]:
				}
			}
		}
	}()

	return rawChan
}

func startREPLSystemNotifications(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader) {
	go func() {
		var done <-chan struct{}
		if kiReader.InputContext != nil {
			done = kiReader.InputContext.Done()
		}
		for {
			select {
			case <-done:
				return
			case event, ok := <-a.SystemEvents:
				if !ok {
					return
				}
				live := a.LiveSettings()
				theme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
				ui.DrawStatusBar(os.Stderr, theme)
				a.SetPendingSystemEvent(event)
				if event != "" && kiReader.IsAtMainPrompt.Load() {
					ui.GetUI().SetPromptHint(fmt.Sprintf("[%s]", event), 3*time.Second, func() {
						if kiReader.InputContext != nil && kiReader.InputContext.Err() != nil {
							return
						}
						if kiReader.IsAtMainPrompt.Load() {
							ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader)
						}
					})
					ui.RefreshConsoleAfterPromptCancellation(os.Stderr, a, kiReader)
				}
			}
		}
	}()
}

func watchREPLResizes(a *agent.Agent, kiReader *interceptor.KeyInterceptorReader) func() {
	sigChan := make(chan os.Signal, 1)
	resizeDone := make(chan struct{})
	var mu sync.Mutex
	closed := false
	var debounceTimer *time.Timer
	terminal.NotifyResize(sigChan)
	go func() {
		for {
			select {
			case <-resizeDone:
				return
			case <-sigChan:
				mu.Lock()
				if closed {
					mu.Unlock()
					return
				}
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
					mu.Lock()
					if closed {
						mu.Unlock()
						return
					}
					defer mu.Unlock()

					terminal.RefreshDimensions()
					if ui.GetUI().IsInModal() {
						return
					}
					if !ui.GetUI().IsTurnActive() {
						if kiReader.IsAtMainPrompt.Load() {
							ui.RedrawScreen(os.Stderr, a, kiReader)
						}
					} else {
						ui.HandleResize(os.Stderr, a, kiReader)
					}
				})
				mu.Unlock()
			}
		}
	}()
	return func() {
		signal.Stop(sigChan)
		mu.Lock()
		closed = true
		if debounceTimer != nil {
			debounceTimer.Stop()
			debounceTimer = nil
		}
		mu.Unlock()
		close(resizeDone)
	}
}

func dispatchREPLSlashCommand(a *agent.Agent, line string, messages *[]db.Message, allowedTools []string, theme *style.UITheme, ppWriter *ui.PromptPreservingWriter, currentSessionID *string, hist interceptor.PromptHistory, mam *swarm.MultiAgentManager, kiReader *interceptor.KeyInterceptorReader) (string, bool, bool, bool) {
	trimmedLine := strings.TrimSpace(line)
	if strings.HasPrefix(trimmedLine, "/") || commands.IsHelpCommand(trimmedLine) {
		parts := strings.Fields(trimmedLine)
		if len(parts) > 0 {
			if expanded, isTpl := commands.ExpandPromptTemplate(a, parts[0], strings.TrimSpace(strings.TrimPrefix(trimmedLine, parts[0]))); isTpl {
				line = expanded
				return line, true, false, false
			}
		}
		// Suspend mouse tracking while canonical slash commands run so wheel
		// scrolling never echoes raw SGR mouse sequences into canonical stdin.
		ui.TerminalMu.Lock()
		resumeMouse := terminal.Default().SuspendMouseTracking()
		ui.TerminalMu.Unlock()
		defer func() {
			ui.TerminalMu.Lock()
			resumeMouse()
			ui.TerminalMu.Unlock()
		}()

		ppWriter.BeginScrollEntry()
		handled, quit := commands.HandleSlashCommand(a, line, messages, allowedTools, theme, ppWriter, currentSessionID, hist, mam, kiReader)
		kiReader.Drain()
		if handled {
			return line, false, true, quit
		}
	}

	return line, false, false, false
}
