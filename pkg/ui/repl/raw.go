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
				select {
				case next := <-rawChan:
					kiReader.ApprovalChan <- b
					kiReader.ApprovalChan <- next
				case <-time.After(50 * time.Millisecond):
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
			if b == 3 || b == 4 {
				abortOperation(cancelFunc, true)
				continue
			}
			if b == 27 {
				select {
				case next := <-rawChan:
					if next == '[' || next == 'O' {
						select {
						case dir := <-rawChan:
							switch dir {
							case 'A':
								kiReader.NavigateHistory(1)
							case 'B':
								kiReader.NavigateHistory(-1)
							case '[':
								select {
								case dir2 := <-rawChan:
									switch dir2 {
									case 'A':
										kiReader.NavigateHistory(1)
									case 'B':
										kiReader.NavigateHistory(-1)
									default:
										kiReader.InputChan <- b
										kiReader.InputChan <- next
										kiReader.InputChan <- dir
										kiReader.InputChan <- dir2
									}
								case <-time.After(20 * time.Millisecond):
									kiReader.InputChan <- b
									kiReader.InputChan <- next
									kiReader.InputChan <- dir
								}
							case 'C', 'D':
							default:
								kiReader.InputChan <- b
								kiReader.InputChan <- next
								kiReader.InputChan <- dir
							}
						case <-time.After(20 * time.Millisecond):
							kiReader.InputChan <- b
							kiReader.InputChan <- next
						}
					} else {
						kiReader.InputChan <- b
						kiReader.InputChan <- next
					}
				case <-time.After(50 * time.Millisecond):
					abortOperation(cancelFunc, true)
				}
				continue
			}

			if b == 16 {
				kiReader.NavigateHistory(1)
				continue
			}
			if b == 14 {
				kiReader.NavigateHistory(-1)
				continue
			}
			if handleShortcutKey(b) {
				continue
			}

			if b == 127 || b == 8 {
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
				continue
			}

			if b == 10 || b == 13 {
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
				continue
			}

			if b >= 32 || b == '\t' {
				ui.GetUI().StateMu.Lock()
				kiReader.TypeAheadBuffer = append(kiReader.TypeAheadBuffer, b)
				ui.GetUI().StateMu.Unlock()
				if len(rawChan) == 0 {
					kiReader.RedrawTypeAhead()
				}
				continue
			}
		}

		kiReader.InputChan <- b
	}
}