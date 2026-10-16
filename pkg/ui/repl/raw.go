package repl

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func readNextRawByte(rawChan <-chan byte, timeout time.Duration) (byte, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case b, ok := <-rawChan:
		if !ok {
			return 0, false
		}
		return b, true
	case <-timer.C:
		return 0, false
	}
}

func forwardToInputChan(kiReader *interceptor.KeyInterceptorReader, data ...byte) {
	for _, b := range data {
		kiReader.InputChan <- b
	}
}

func handleActiveTurnEscape(rawChan <-chan byte, kiReader *interceptor.KeyInterceptorReader, esc byte, onBareEscape func()) {
	next, ok := readNextRawByte(rawChan, 50*time.Millisecond)
	if !ok {
		onBareEscape()
		return
	}
	if next != '[' && next != 'O' {
		forwardToInputChan(kiReader, esc, next)
		return
	}

	dir, ok := readNextRawByte(rawChan, 20*time.Millisecond)
	if !ok {
		forwardToInputChan(kiReader, esc, next)
		return
	}

	switch dir {
	case 'A':
		kiReader.NavigateHistory(1)
	case 'B':
		kiReader.NavigateHistory(-1)
	case '[':
		dir2, ok := readNextRawByte(rawChan, 20*time.Millisecond)
		if !ok {
			forwardToInputChan(kiReader, esc, next, dir)
			return
		}
		switch dir2 {
		case 'A':
			kiReader.NavigateHistory(1)
		case 'B':
			kiReader.NavigateHistory(-1)
		default:
			forwardToInputChan(kiReader, esc, next, dir, dir2)
		}
	case 'C', 'D':
	default:
		forwardToInputChan(kiReader, esc, next, dir)
	}
}

func startREPLRawInputDispatcher(rawChan <-chan byte, kiReader *interceptor.KeyInterceptorReader) {
	abortOperation := func(cancelFunc func(), clearQueue bool) {
		if cancelFunc != nil {
			cancelFunc()
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		kiReader.ResetTypeAheadLocked()
		ui.GetUI().StateMu.Unlock()
		kiReader.PrintCancelMessage()

		if clearQueue {
			cleared := kiReader.ClearQueue()
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = 0
			ui.GetUI().StateMu.Unlock()
			activeTheme := ui.GetConfiguredTheme(kiReader.Agent.Config)
			ui.DrawStatusBar(os.Stderr, activeTheme)
			if cleared > 0 {
				output := kiReader.W
				if kiReader.Agent != nil && kiReader.Agent.CurrentWriter() != nil {
					output = kiReader.Agent.CurrentWriter()
				} else if kiReader.Agent != nil {
					if uiImpl, ok := kiReader.Agent.UI.(*ui.AgentUIImpl); ok && uiImpl.PPWriter != nil {
						output = uiImpl.PPWriter
					}
				}
				qStyle := style.NewStyle().Foreground(activeTheme.Border).Italic(true)
				fmt.Fprintf(output, "\n%s\n", qStyle.Render(fmt.Sprintf("[queue cleared: %d item(s)]", cleared)))
			}
		}
	}

	handleShortcutKey := func(b byte) bool {
		switch b {
		case 7:
			kiReader.HandleCtrlG()
		case 15:
			kiReader.HandleCtrlO()
		case 18:
			kiReader.HandleCtrlR()
		case 20:
			kiReader.HandleCtrlT()
		default:
			return false
		}
		return true
	}

	for {
		b, ok := <-rawChan
		if !ok {
			close(kiReader.InputChan)
			close(kiReader.ApprovalChan)
			return
		}

		ui.GetUI().StateMu.Lock()
		cancelFunc := ui.GetUI().ActiveCancelFunc
		inApproval := ui.GetUI().InApprovalPrompt
		ui.GetUI().StateMu.Unlock()

		if inApproval {
			if b == 3 || b == 4 {
				abortOperation(cancelFunc, false)
				kiReader.ApprovalChan <- b
				continue
			}
			if b == 27 {
				if next, ok := readNextRawByte(rawChan, 50*time.Millisecond); ok {
					kiReader.ApprovalChan <- b
					kiReader.ApprovalChan <- next
				} else {
					abortOperation(cancelFunc, false)
					kiReader.ApprovalChan <- b
				}
				continue
			}
			if handleShortcutKey(b) {
				continue
			}
			kiReader.ApprovalChan <- b
			continue
		}

		if (b == 3 || b == 4) && kiReader.MAM != nil {
			if activeName, active := kiReader.MAM.ActiveSubagentName(); active {
				kiReader.HandleSubagentCancellation(rawChan, cancelFunc, activeName)
				continue
			}
		}

		if cancelFunc != nil {
			switch {
			case b == 3 || b == 4:
				abortOperation(cancelFunc, true)
			case b == 27:
				handleActiveTurnEscape(rawChan, kiReader, b, func() {
					abortOperation(cancelFunc, true)
				})
			case b == 16:
				kiReader.NavigateHistory(1)
			case b == 14:
				kiReader.NavigateHistory(-1)
			case handleShortcutKey(b):
			case b == 127 || b == 8:
				ui.GetUI().StateMu.Lock()
				if len(kiReader.TypeAheadBuffer) > 0 {
					r, size := utf8.DecodeLastRune(kiReader.TypeAheadBuffer)
					if r != utf8.RuneError || size > 0 {
						kiReader.TypeAheadBuffer = kiReader.TypeAheadBuffer[:len(kiReader.TypeAheadBuffer)-size]
					} else {
						kiReader.TypeAheadBuffer = kiReader.TypeAheadBuffer[:len(kiReader.TypeAheadBuffer)-1]
					}
				}
				ui.GetUI().StateMu.Unlock()
				if len(rawChan) == 0 {
					kiReader.RedrawTypeAhead()
				}
			case b == 10 || b == 13:
				ui.GetUI().StateMu.Lock()
				typed := string(kiReader.TypeAheadBuffer)
				kiReader.ResetTypeAheadLocked()
				ui.GetUI().StateMu.Unlock()

				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					count := kiReader.EnqueuePrompt(trimmed)
					ui.GetUI().StateMu.Lock()
					ui.GetUI().State.QueuedPromptsCount = count
					ui.GetUI().StateMu.Unlock()
					ui.DrawStatusBar(os.Stderr, ui.GetConfiguredTheme(kiReader.Agent.Config))
				}
				kiReader.RedrawTypeAhead()
			case b >= 32 || b == '\t':
				ui.GetUI().StateMu.Lock()
				kiReader.TypeAheadBuffer = append(kiReader.TypeAheadBuffer, b)
				ui.GetUI().StateMu.Unlock()
				if len(rawChan) == 0 {
					kiReader.RedrawTypeAhead()
				}
			}
			continue
		}

		kiReader.InputChan <- b
	}
}