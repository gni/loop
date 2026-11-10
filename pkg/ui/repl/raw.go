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

func forwardApprovalBytes(ki *interceptor.KeyInterceptorReader, data ...byte) bool {
	var done <-chan struct{}
	if ki.InputContext != nil {
		done = ki.InputContext.Done()
	}
	for _, b := range data {
		for {
			state := ui.GetUI()
			state.StateMu.Lock()
			active := state.InApprovalPrompt
			state.StateMu.Unlock()
			if !active {
				return false
			}
			sent := false
			select {
			case <-done:
				return false
			case ki.ApprovalChan <- b:
				sent = true
			default:
			}
			if sent {
				break
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-done:
				timer.Stop()
				return false
			case ki.ApprovalChan <- b:
				timer.Stop()
			case <-timer.C:
				continue
			}
			break
		}
	}
	return true
}

// Keep paste contents out of shortcut dispatch, even if the prompt closes
// while a slow paste is arriving. Its remaining bytes are then discarded.
func forwardApprovalEscape(raw <-chan byte, ki *interceptor.KeyInterceptorReader, onBareEscape func()) bool {
	sequence := []byte{27}
	next, ok := readNextRawByte(raw, 50*time.Millisecond)
	if !ok {
		onBareEscape()
		forwardApprovalBytes(ki, sequence...)
		return true
	}
	sequence = append(sequence, next)
	if next == '[' || next == 'O' {
		for len(sequence) < 64 {
			b, ok := readNextRawByte(raw, 50*time.Millisecond)
			if !ok {
				break
			}
			sequence = append(sequence, b)
			if b >= 0x40 && b <= 0x7e {
				break
			}
		}
	}
	deliver := forwardApprovalBytes(ki, sequence...)
	if string(sequence) != "\x1b[200~" {
		return true
	}
	var done <-chan struct{}
	if ki.InputContext != nil {
		done = ki.InputContext.Done()
	}
	tail := ""
	for {
		var b byte
		select {
		case <-done:
			return false
		case next, ok := <-raw:
			if !ok {
				return false
			}
			b = next
		}
		if deliver {
			deliver = forwardApprovalBytes(ki, b)
		}
		tail += string([]byte{b})
		if len(tail) > 6 {
			tail = tail[len(tail)-6:]
		}
		if tail == "\x1b[201~" {
			return true
		}
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
	case '2':
		sequence := []byte{esc, next, dir}
		for len(sequence) < 8 && sequence[len(sequence)-1] != '~' {
			b, ok := readNextRawByte(rawChan, 50*time.Millisecond)
			if !ok {
				break
			}
			sequence = append(sequence, b)
		}
		if string(sequence) != "\x1b[200~" {
			forwardToInputChan(kiReader, sequence...)
			return
		}
		var pasted []byte
		for b := range rawChan {
			pasted = append(pasted, b)
			if strings.HasSuffix(string(pasted[max(0, len(pasted)-6):]), "\x1b[201~") {
				pasted = pasted[:len(pasted)-6]
				text := interceptor.NormalizePastedText(pasted)
				ui.GetUI().StateMu.Lock()
				active := ui.GetUI().ActiveCancelFunc != nil
				if active {
					kiReader.TypeAheadBuffer = append(kiReader.TypeAheadBuffer, text...)
					kiReader.TypeAheadPastes = append(kiReader.TypeAheadPastes, text)
				}
				ui.GetUI().StateMu.Unlock()
				if active {
					kiReader.RedrawTypeAhead()
				} else {
					// The model may finish before a slow paste completes.
					forwardToInputChan(kiReader, sequence...)
					forwardToInputChan(kiReader, pasted...)
					forwardToInputChan(kiReader, []byte("\x1b[201~")...)
				}
				return
			}
		}
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
	rawChan = normalizeKeyboardInput(rawChan, kiReader.OnChatHistoryInput)
	defer close(kiReader.InputChan)
	defer close(kiReader.ApprovalChan)
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
			return
		}

		ui.GetUI().StateMu.Lock()
		cancelFunc := ui.GetUI().ActiveCancelFunc
		inApproval := ui.GetUI().InApprovalPrompt
		ui.GetUI().StateMu.Unlock()

		if inApproval {
			if b == 30 {
				b = 13
			}
			if b == 3 || b == 4 {
				abortOperation(cancelFunc, false)
				forwardApprovalBytes(kiReader, b)
				continue
			}
			if b == 27 {
				if !forwardApprovalEscape(rawChan, kiReader, func() { abortOperation(cancelFunc, false) }) {
					return
				}
				continue
			}
			if handleShortcutKey(b) {
				continue
			}
			forwardApprovalBytes(kiReader, b)
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
			case b == 26:
				kiReader.ControlQueue("undo")
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
			case b == 14 || b == 30 || b == 10:
				ui.GetUI().StateMu.Lock()
				kiReader.TypeAheadBuffer = append(kiReader.TypeAheadBuffer, '\n')
				ui.GetUI().StateMu.Unlock()
				kiReader.RedrawTypeAhead()
			case b == 13:
				ui.GetUI().StateMu.Lock()
				typed := string(kiReader.TypeAheadBuffer)
				pastes := append([]string(nil), kiReader.TypeAheadPastes...)
				kiReader.ResetTypeAheadLocked()
				ui.GetUI().StateMu.Unlock()

				// Queue controls act immediately while generation continues.
				// Clipboard text remains literal, including pasted slash examples.
				source, pasted := interceptor.PastedInputSource(typed, pastes)
				parts := strings.Fields(source)
				if !pasted && len(parts) == 2 && (parts[0] == "/queue" || parts[0] == "/queues") {
					if _, handled := kiReader.ControlQueue(parts[1]); handled {
						kiReader.RedrawTypeAhead()
						continue
					}
				}

				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					count := kiReader.EnqueuePrompt(trimmed, pastes)
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
