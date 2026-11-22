package repl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"loop/pkg/domain/limits"
	"loop/pkg/terminal"
	"loop/pkg/ui"
	"loop/pkg/ui/input"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

const activePasteIdleTimeout = 2 * time.Second

func readNextRawByte(ctx context.Context, rawChan <-chan byte, timeout time.Duration) (byte, bool) {
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return 0, false
	case b, ok := <-rawChan:
		if !ok {
			return 0, false
		}
		return b, true
	case <-timer.C:
		return 0, false
	}
}

func forwardToInputChan(kiReader *interceptor.KeyInterceptorReader, data ...byte) bool {
	var done <-chan struct{}
	if kiReader.InputContext != nil {
		done = kiReader.InputContext.Done()
	}
	for _, b := range data {
		select {
		case <-done:
			return false
		case kiReader.InputChan <- b:
			continue
		default:
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-done:
			timer.Stop()
			return false
		case kiReader.InputChan <- b:
			timer.Stop()
		case <-timer.C:
			// Drop excess bytes if InputChan is stalled so Ctrl+C / ApprovalChan never starve.
			return true
		}
	}
	return true
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

func readCompleteEscapeSequence(ctx context.Context, raw <-chan byte, esc byte) ([]byte, bool) {
	timeout := limits.EscapeDisambiguation()
	next, ok := readNextRawByte(ctx, raw, timeout)
	if !ok {
		return []byte{esc}, false
	}
	seq := make([]byte, 2, 16)
	seq[0] = esc
	seq[1] = next

	switch next {
	case '[':
		first, ok := readNextRawByte(ctx, raw, timeout)
		if !ok {
			return seq, true
		}
		seq = append(seq, first)
		if first == 'M' {
			for range 3 {
				mb, mok := readNextRawByte(ctx, raw, timeout)
				if !mok {
					break
				}
				seq = append(seq, mb)
			}
			return seq, true
		}
		if first == '[' {
			// Linux console CSI-[A..E sequences carry one extra byte after the second '['.
			if extra, ok := readNextRawByte(ctx, raw, timeout); ok {
				seq = append(seq, extra)
			}
			return seq, true
		}
		b := first
		for len(seq) < 64 && !(b >= 0x40 && b <= 0x7e) {
			nb, nok := readNextRawByte(ctx, raw, timeout)
			if !nok {
				break
			}
			seq = append(seq, nb)
			b = nb
		}
		return seq, true
	case 'O':
		if third, ok := readNextRawByte(ctx, raw, timeout); ok {
			seq = append(seq, third)
		}
		return seq, true
	case ']', 'P', 'X', '^', '_':
		prevEsc := false
		for range 4096 {
			b, bok := readNextRawByte(ctx, raw, timeout)
			if !bok {
				break
			}
			if next == ']' && b == '\a' {
				break
			}
			if prevEsc && b == '\\' {
				break
			}
			prevEsc = b == '\x1b'
		}
		return seq, true
	default:
		return seq, true
	}
}

func readBoundedBracketedPaste(ctx context.Context, raw <-chan byte) ([]byte, bool) {
	endSeq := []byte(input.BracketedPasteEnd)
	var body []byte
	var tail []byte
	for {
		if ctx != nil && ctx.Err() != nil {
			return body, false
		}
		b, ok := readNextRawByte(ctx, raw, activePasteIdleTimeout)
		if !ok {
			if len(tail) > 0 && len(body) < input.MaxPasteBytes {
				rem := input.MaxPasteBytes - len(body)
				if len(tail) > rem {
					tail = tail[:rem]
				}
				body = append(body, tail...)
			}
			return body, false
		}
		tail = append(tail, b)
		if len(tail) > len(endSeq) {
			if len(body) < input.MaxPasteBytes {
				body = append(body, tail[0])
			}
			tail = tail[1:]
		}
		if len(tail) == len(endSeq) && bytes.Equal(tail, endSeq) {
			return body, true
		}
	}
}

// Keep paste contents out of shortcut dispatch, even if the prompt closes
// while a slow paste is arriving. Its remaining bytes are then discarded.
func forwardApprovalEscape(raw <-chan byte, ki *interceptor.KeyInterceptorReader, onBareEscape func()) bool {
	sequence, hasFollowup := readCompleteEscapeSequence(ki.InputContext, raw, 27)
	if !hasFollowup {
		onBareEscape()
		forwardApprovalBytes(ki, sequence...)
		return true
	}
	deliver := forwardApprovalBytes(ki, sequence...)
	if string(sequence) != input.BracketedPasteStart {
		return true
	}
	body, complete := readBoundedBracketedPaste(ki.InputContext, raw)
	if !complete && ki.InputContext != nil && ki.InputContext.Err() != nil {
		return false
	}
	if deliver {
		if forwardApprovalBytes(ki, body...) {
			forwardApprovalBytes(ki, []byte(input.BracketedPasteEnd)...)
		}
	}
	return true
}

func handleActiveTurnEscape(rawChan <-chan byte, kiReader *interceptor.KeyInterceptorReader, esc byte, onBareEscape func()) {
	sequence, hasFollowup := readCompleteEscapeSequence(kiReader.InputContext, rawChan, esc)
	if !hasFollowup {
		onBareEscape()
		return
	}

	seqStr := string(sequence)
	switch seqStr {
	case input.BracketedPasteStart:
		pasted, complete := readBoundedBracketedPaste(kiReader.InputContext, rawChan)
		if !complete && kiReader.InputContext != nil && kiReader.InputContext.Err() != nil {
			return
		}
		text := input.NormalizePastedText(pasted)
		ui.GetUI().StateMu.Lock()
		active := ui.GetUI().ActiveCancelFunc != nil
		ui.GetUI().StateMu.Unlock()
		if active {
			kiReader.AppendTypeAheadPaste(text)
			kiReader.RedrawTypeAhead()
		} else {
			// The model may finish before a slow paste completes.
			if forwardToInputChan(kiReader, sequence...) {
				if forwardToInputChan(kiReader, []byte(text)...) {
					forwardToInputChan(kiReader, []byte(input.BracketedPasteEnd)...)
				}
			}
		}
	case "\x1b[A", "\x1bOA", "\x1b[[A":
		kiReader.NavigateHistory(1)
	case "\x1b[B", "\x1bOB", "\x1b[[B":
		kiReader.NavigateHistory(-1)
	case "\x1b\r", "\x1b\n":
		kiReader.AppendTypeAheadByte('\n')
		kiReader.RedrawTypeAhead()
	default:
		// Complete unhandled escape sequences (Delete, Ctrl+Left, F5, PgUp, mouse, etc.)
		// are intentionally consumed intact so trailing bytes never leak into TypeAheadBuffer.
	}
}

func startREPLRawInputDispatcher(rawChan <-chan byte, kiReader *interceptor.KeyInterceptorReader) {
	rawChan = normalizeKeyboardInput(kiReader.InputContext, rawChan, kiReader.OnChatHistoryInput)
	defer close(kiReader.InputChan)
	defer close(kiReader.ApprovalChan)

	var done <-chan struct{}
	if kiReader.InputContext != nil {
		done = kiReader.InputContext.Done()
	}

	resolveTheme := func() style.UITheme {
		live := kiReader.Agent.LiveSettings()
		return style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	}

	abortOperation := func(cancelFunc func(), clearQueue bool) {
		if cancelFunc != nil {
			cancelFunc()
		}
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
		kiReader.ResetTypeAhead()

		if clearQueue {
			cleared := kiReader.ClearQueue()
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = 0
			ui.GetUI().StateMu.Unlock()
			activeTheme := resolveTheme()
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
		var b byte
		var ok bool
		select {
		case <-done:
			return
		case b, ok = <-rawChan:
			if !ok {
				return
			}
		}

		ui.GetUI().StateMu.Lock()
		cancelFunc := ui.GetUI().ActiveCancelFunc
		inApproval := ui.GetUI().InApprovalPrompt
		inMenu := ui.GetUI().InInteractiveMenu
		ui.GetUI().StateMu.Unlock()
		inModal := inApproval || inMenu || terminal.Default().InAltScreen()

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
			forwardApprovalBytes(kiReader, b)
			continue
		}

		if (b == 3 || b == 4) && !inModal && kiReader.MAM != nil {
			if activeName, active := kiReader.MAM.ActiveSubagentName(); active {
				kiReader.HandleSubagentCancellation(rawChan, cancelFunc, activeName)
				continue
			}
		}

		if cancelFunc != nil && !inModal {
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
				kiReader.BackspaceTypeAhead()
				if len(rawChan) == 0 {
					kiReader.RedrawTypeAhead()
				}
			case b == 14 || b == 30 || b == 10:
				kiReader.AppendTypeAheadByte('\n')
				kiReader.RedrawTypeAhead()
			case b == 13:
				buf, pastes := kiReader.TakeTypeAhead()
				typed := string(buf)

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
					ui.DrawStatusBar(os.Stderr, resolveTheme())
				}
				kiReader.RedrawTypeAhead()
			case b >= 32 || b == '\t':
				kiReader.AppendTypeAheadByte(b)
				if len(rawChan) == 0 {
					kiReader.RedrawTypeAhead()
				}
			}
			continue
		}

		if !forwardToInputChan(kiReader, b) {
			return
		}
	}
}
