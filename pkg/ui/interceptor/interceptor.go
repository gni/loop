package interceptor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/ui/style"
)

// PromptHistory matches ui.CustomHistory without coupling to golang.org/x/term.
type PromptHistory interface {
	Add(entry string)
	At(idx int) string
	Len() int
}

type KeyInterceptorReader struct {
	InputContext          context.Context
	R                     io.Reader
	Agent                 *agent.Agent
	Theme                 style.UITheme
	W                     io.Writer
	DraftMu               sync.Mutex
	CurrentInputLine      string
	CurrentInputPos       int
	PendingLineReplace    string
	HasPendingLineReplace bool
	Messages              *[]db.Message
	PasteRemaining        []byte
	InputRemaining        []byte // Unprocessed input after a completed paste frame.
	PasteStartPending     []byte
	PastedText            string
	CtrlCInterrupted      bool
	MAM                   *swarm.MultiAgentManager
	AllowedTools          []string
	InputChan             chan byte
	ApprovalChan          chan byte
	ApprovalReader        *ApprovalByteReader
	approvalCancel        func()
	InjectChan            chan byte
	TypeAheadBuffer       []byte
	TypeAheadPastes       []string
	HistoryIndex          int
	SavedTypeAhead        []byte
	Hist                  PromptHistory
	InBracketedPaste      bool
	BracketedBuffer       []byte
	bracketedTruncated    bool
	bracketedTail         []byte
	IsAtMainPrompt        atomic.Bool
	PastedCodeBlocks      map[string]string
	PendingPastes         []string // Bulk edits consumed by the foreground prompt editor.
	PastedLiterals        []string // Clipboard text or previews in the current prompt.
	PromptQueue           []PromptInput
	PromptQueueMu         sync.Mutex
	PasteCounter          int

	OnClearPromptHint       func()
	OnRedrawScreen          func()
	OnDrawStaticControls    func(w io.Writer)
	OnRedrawPromptSeparator func()
	OnRedrawStatusBar       func()
	OnRedrawTypeAhead       func()
	OnThemeChanged          func(name string, theme style.UITheme)
	OnClearCancelFunc       func()
	OnChatHistoryInput      func([]byte) []byte
	OnQueueChanged          func()
	OnUserInput             func()

	ActiveTurn             atomic.Bool   // A foreground turn is still streaming.
	PendingCollapseToggles atomic.Uint64 // Apply active Ctrl+O presses on the foreground renderer.
	PendingThemeRefresh    atomic.Bool   // Repaint active turn after mid-turn theme switch.
}

// ConsumePendingThemeRefresh reports whether a theme switch arrived during an
// active turn and clears the pending flag atomically.
func (ki *KeyInterceptorReader) ConsumePendingThemeRefresh() bool {
	if ki == nil {
		return false
	}
	return ki.PendingThemeRefresh.Swap(false)
}

func (ki *KeyInterceptorReader) copyReadData(p, data []byte) int {
	n := copy(p, data)
	if n < len(data) {
		ki.PasteRemaining = append([]byte(nil), data[n:]...)
	}
	return n
}

func (ki *KeyInterceptorReader) SetPromptDraft(line string, runePos int) {
	ki.DraftMu.Lock()
	ki.CurrentInputLine = line
	ki.CurrentInputPos = runePos
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) GetPromptDraft() (string, int) {
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	return ki.CurrentInputLine, ki.CurrentInputPos
}

func (ki *KeyInterceptorReader) HasPromptDraft() bool {
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	return ki.CurrentInputLine != ""
}

func (ki *KeyInterceptorReader) SetPendingLineReplace(text string) {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.PendingLineReplace = text
	ki.HasPendingLineReplace = true
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) TakePendingLineReplace() (string, bool) {
	if ki == nil {
		return "", false
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	if !ki.HasPendingLineReplace {
		return "", false
	}
	text := ki.PendingLineReplace
	ki.PendingLineReplace = ""
	ki.HasPendingLineReplace = false
	return text, true
}

func (ki *KeyInterceptorReader) GetPastedText() string {
	if ki == nil {
		return ""
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	return ki.PastedText
}

func (ki *KeyInterceptorReader) SetPastedText(text string) {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.PastedText = text
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) TakePastedText() string {
	if ki == nil {
		return ""
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	text := ki.PastedText
	ki.PastedText = ""
	return text
}

func (ki *KeyInterceptorReader) GetTheme() style.UITheme {
	if ki == nil {
		return style.UITheme{}
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	return ki.Theme
}

func (ki *KeyInterceptorReader) SetTheme(theme style.UITheme) {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.Theme = theme
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) GetAllowedTools() []string {
	if ki == nil {
		return nil
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	if ki.AllowedTools == nil {
		return nil
	}
	copied := make([]string, len(ki.AllowedTools))
	copy(copied, ki.AllowedTools)
	return copied
}

func (ki *KeyInterceptorReader) SetAllowedTools(tools []string) {
	if ki == nil {
		return
	}
	var copied []string
	if tools != nil {
		copied = make([]string, len(tools))
		copy(copied, tools)
	}
	ki.DraftMu.Lock()
	ki.AllowedTools = copied
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) ResetTypeAhead() {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.resetTypeAheadLocked()
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) resetTypeAheadLocked() {
	ki.TypeAheadBuffer = nil
	ki.TypeAheadPastes = nil
	ki.HistoryIndex = -1
	ki.SavedTypeAhead = nil
}

func (ki *KeyInterceptorReader) GetTypeAheadDraft() (string, int) {
	if ki == nil {
		return "", 0
	}
	ki.DraftMu.Lock()
	text := string(ki.TypeAheadBuffer)
	ki.DraftMu.Unlock()
	return text, len([]rune(text))
}

func (ki *KeyInterceptorReader) TakeTypeAhead() ([]byte, []string) {
	if ki == nil {
		return nil, nil
	}
	ki.DraftMu.Lock()
	buf := append([]byte(nil), ki.TypeAheadBuffer...)
	pastes := append([]string(nil), ki.TypeAheadPastes...)
	ki.resetTypeAheadLocked()
	ki.DraftMu.Unlock()
	return buf, pastes
}

func (ki *KeyInterceptorReader) AppendTypeAheadByte(b byte) {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.TypeAheadBuffer = append(ki.TypeAheadBuffer, b)
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) AppendTypeAheadPaste(text string) {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.TypeAheadBuffer = append(ki.TypeAheadBuffer, text...)
	ki.TypeAheadPastes = append(ki.TypeAheadPastes, text)
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) ResetHistoryIndex() {
	if ki == nil {
		return
	}
	ki.DraftMu.Lock()
	ki.HistoryIndex = -1
	ki.DraftMu.Unlock()
}

func (ki *KeyInterceptorReader) GetHistoryIndex() int {
	if ki == nil {
		return -1
	}
	ki.DraftMu.Lock()
	defer ki.DraftMu.Unlock()
	return ki.HistoryIndex
}

func drainByteChannel(ch <-chan byte) {
	if ch == nil {
		return
	}
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		default:
			return
		}
	}
}

func (ki *KeyInterceptorReader) Drain() {
	drainByteChannel(ki.InputChan)
	drainByteChannel(ki.InjectChan)
	ki.DraftMu.Lock()
	ki.PendingLineReplace = ""
	ki.HasPendingLineReplace = false
	ki.DraftMu.Unlock()
	ki.PendingPastes = nil
	ki.PastedLiterals = nil
	ki.PasteRemaining = nil
	ki.InputRemaining = nil
	ki.PasteStartPending = nil
	ki.BracketedBuffer = nil
	ki.bracketedTruncated = false
	ki.bracketedTail = nil
	ki.InBracketedPaste = false
}

func (ki *KeyInterceptorReader) resolveTargetWriter(fallback io.Writer) io.Writer {
	if ki.W != nil {
		return ki.W
	}
	if w, ok := ki.R.(io.Writer); ok {
		return w
	}
	return fallback
}

func (ki *KeyInterceptorReader) Write(p []byte) (int, error) {
	live := ki.Agent.LiveSettings()
	activeTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
	promptPrefix := "▹ "
	if ki.MAM != nil {
		if name := ki.MAM.ActiveAgentName(); name != "" {
			promptPrefix = fmt.Sprintf("[%s]%s", name, promptPrefix)
		}
	}
	promptStr := style.NewStyle().Foreground(activeTheme.Primary).Bold(true).Render(promptPrefix)

	p = StylePrompt(p, promptPrefix, promptStr)

	if bytes.Contains(p, []byte("\x1b[2J")) {
		if ki.OnRedrawScreen != nil {
			ki.OnRedrawScreen()
		}
		cleaned := p
		for _, seq := range []string{"\x1b[H\x1b[2J", "\x1b[2J\x1b[H", "\x1b[2J", "\x1b[H", "\x1b[1;1H"} {
			cleaned = bytes.ReplaceAll(cleaned, []byte(seq), nil)
		}
		if len(cleaned) > 0 {
			if _, err := ki.writeToTerminal(cleaned); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}

	if bytes.Contains(p, []byte("\x1b[H")) || bytes.Contains(p, []byte("\x1b[1;1H")) {
		return len(p), nil
	}

	if ki.IsAtMainPrompt.Load() {
		if ki.OnDrawStaticControls != nil {
			ki.OnDrawStaticControls(ki.resolveTargetWriter(os.Stderr))
		}
		return len(p), nil
	}

	return ki.writeToTerminal(p)
}

func (ki *KeyInterceptorReader) writeToTerminal(p []byte) (int, error) {
	return ki.resolveTargetWriter(os.Stdout).Write(p)
}

func (ki *KeyInterceptorReader) GetInputContext() context.Context {
	return ki.InputContext
}

func (ki *KeyInterceptorReader) GetInputChan() chan byte {
	return ki.InputChan
}

func (ki *KeyInterceptorReader) readFromChannels(p []byte) (int, error) {
	var done <-chan struct{}
	if ki.InputContext != nil {
		done = ki.InputContext.Done()
	}
	var b byte
	select {
	case <-done:
		return 0, ki.InputContext.Err()
	case injected, ok := <-ki.InjectChan:
		if !ok {
			return 0, io.EOF
		}
		b = injected
	case input, ok := <-ki.InputChan:
		if !ok {
			return 0, io.EOF
		}
		b = input
	}

	if ki.OnUserInput != nil {
		cb := ki.OnUserInput
		ki.OnUserInput = nil
		cb()
	}

	if b != 4 && ki.OnClearPromptHint != nil {
		ki.OnClearPromptHint()
	}

	p[0] = b
	n := 1

	for n < len(p) {
		select {
		case injected, ok := <-ki.InjectChan:
			if !ok {
				return n, nil
			}
			p[n] = injected
			n++
		default:
			goto readInputChannel
		}
	}
	return n, nil

readInputChannel:
	var escTimer *time.Timer
	defer func() {
		if escTimer != nil {
			escTimer.Stop()
		}
	}()

	for n < len(p) {
		if hasIncompleteEscape(p[:n]) {
			timeout := limits.EscapeDisambiguation()
			if escTimer == nil {
				escTimer = time.NewTimer(timeout)
			} else {
				if !escTimer.Stop() {
					select {
					case <-escTimer.C:
					default:
					}
				}
				escTimer.Reset(timeout)
			}
			select {
			case <-done:
				return n, nil
			case input, ok := <-ki.InputChan:
				if !ok {
					return n, nil
				}
				p[n] = input
				n++
			case <-escTimer.C:
				return n, nil
			}
		} else {
			select {
			case input, ok := <-ki.InputChan:
				if !ok {
					return n, nil
				}
				p[n] = input
				n++
			default:
				return n, nil
			}
		}
	}

	return n, nil
}

// hasIncompleteEscape reports whether data ends with a partial ANSI escape
// sequence that may still have bytes in flight. Complete sequences such as
// "\x1b[A", "\x1b[3~", or "\x1bOH" return false immediately with zero wait.
func hasIncompleteEscape(data []byte) bool {
	idx := bytes.LastIndexByte(data, '\x1b')
	if idx < 0 {
		return false
	}
	tail := data[idx:]
	if len(tail) == 1 {
		return true
	}
	switch tail[1] {
	case '[':
		if len(tail) == 2 {
			return true
		}
		if tail[2] == 'M' {
			return len(tail) < 6
		}
		for i := 2; i < len(tail); i++ {
			if tail[i] >= 0x40 && tail[i] <= 0x7e {
				return false
			}
		}
		return len(tail) < 64
	case 'O':
		return len(tail) < 3
	case ']', 'P', 'X', '^', '_':
		for i := 2; i < len(tail); i++ {
			if tail[i] == '\a' {
				return false
			}
		}
		return len(tail) < 64
	default:
		if tail[1] >= 0x20 && tail[1] <= 0x2f {
			i := 1
			for i < len(tail) && tail[i] >= 0x20 && tail[i] <= 0x2f {
				i++
			}
			return i >= len(tail)
		}
		return false
	}
}

func (ki *KeyInterceptorReader) Read(p []byte) (int, error) {
readInput:
	if ki.InputContext != nil && ki.InputContext.Err() != nil {
		return 0, ki.InputContext.Err()
	}
	if len(ki.PasteRemaining) > 0 {
		n := copy(p, ki.PasteRemaining)
		ki.PasteRemaining = ki.PasteRemaining[n:]
		return n, nil
	}

	var n int
	var err error
	if len(ki.InputRemaining) > 0 {
		n = copy(p, ki.InputRemaining)
		ki.InputRemaining = ki.InputRemaining[n:]
	} else if ki.InputChan == nil {
		if ki.R == nil {
			return 0, io.EOF
		}
		n, err = ki.R.Read(p)
		if n > 0 && ki.OnUserInput != nil {
			cb := ki.OnUserInput
			ki.OnUserInput = nil
			cb()
		}
	} else {
		n, err = ki.readFromChannels(p)
	}
	if err != nil {
		return n, err
	}

	n, pasted := ki.handlePasteData(p, n)
	if pasted && n == 0 {
		goto readInput
	}
	if pasted {
		return n, nil
	}
	n = ki.copyReadData(p, ki.normalizePromptEditKeys(p[:n]))

	writeIdx := 0
	for i := 0; i < n; i++ {
		b := p[i]
		switch b {
		case 3:
			if ki.Agent != nil {
				ki.Agent.TasksMu.Lock()
				if ki.Agent.StreamingTask != "" {
					streamingID := ki.Agent.StreamingTask
					ki.Agent.StreamingTask = ""
					ki.Agent.TasksMu.Unlock()
					dest := io.Writer(os.Stderr)
					if chatWriter := ki.Agent.GetChatWriter(); chatWriter != nil && chatWriter != os.Stderr {
						dest = chatWriter
					}
					fmt.Fprintf(dest, "\n[stopped streaming %s]\n", streamingID)
					if ki.IsAtMainPrompt.Load() {
						ki.CtrlCInterrupted = true
						p[writeIdx] = '\n'
						writeIdx++
					}
					continue
				}
				ki.Agent.TasksMu.Unlock()
			}
			if ki.IsAtMainPrompt.Load() {
				ki.CtrlCInterrupted = true
				p[writeIdx] = '\n'
			} else {
				p[writeIdx] = b
			}
			writeIdx++
		case 7:
			ki.HandleCtrlG()
		case 15:
			ki.HandleCtrlO()
		case 18:
			ki.HandleCtrlR()
		case 20:
			ki.HandleCtrlT()
		case 25:
			ki.HandleCtrlY()
		case 26:
			ki.ControlQueue("undo")
		default:
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}
