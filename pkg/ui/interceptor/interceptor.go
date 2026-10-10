package interceptor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

type KeyInterceptorReader struct {
	R                io.Reader
	Agent            *agent.Agent
	Theme            style.UITheme
	W                io.Writer
	RL               *term.Terminal
	CurrentInputLine string
	CurrentInputPos  int
	Messages         *[]db.Message
	PasteRemaining   []byte
	PastedText       string
	CtrlCInterrupted bool
	MAM              *swarm.MultiAgentManager
	AllowedTools     []string
	InputChan        chan byte
	ApprovalChan     chan byte
	ApprovalReader   *ApprovalByteReader
	InjectChan       chan byte
	TypeAheadBuffer  []byte
	HistoryIndex     int
	SavedTypeAhead   []byte
	Hist             term.History
	InBracketedPaste bool
	BracketedBuffer  []byte
	IsAtMainPrompt   bool
	LastCtrlDTime    time.Time
	PastedCodeBlocks map[string]string
	PromptQueue      []string
	PromptQueueMu    sync.Mutex
	PasteLinesOffset int
	PasteCounter     int

	// Callbacks for decoupling from parent UI
	OnClearPromptHint        func()
	OnRedrawScreen           func()
	OnDrawStaticControls     func(w io.Writer)
	OnRedrawPromptSeparator func()
	OnClearCancelFunc        func()
}

func (ki *KeyInterceptorReader) copyReadData(p, data []byte) int {
	n := copy(p, data)
	if n < len(data) {
		ki.PasteRemaining = append([]byte(nil), data[n:]...)
	}
	return n
}

func (ki *KeyInterceptorReader) Drain() {
	if ki.InputChan != nil {
		for {
			select {
			case <-ki.InputChan:
			default:
				goto drainInject
			}
		}
	}
drainInject:
	if ki.InjectChan != nil {
		for {
			select {
			case <-ki.InjectChan:
			default:
				return
			}
		}
	}
}

func (ki *KeyInterceptorReader) Write(p []byte) (int, error) {
	var cfg *config.Config
	if ki.Agent != nil {
		cfg = ki.Agent.Config
	}
	activeTheme := style.ResolveConfiguredTheme("", "")
	promptPrefix := "> "
	if cfg != nil {
		activeTheme = style.ResolveConfiguredTheme(cfg.Theme, cfg.SyntaxTheme)
	}
	if ki.MAM != nil && ki.MAM.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", ki.MAM.ActiveAgent.Name, promptPrefix)
	}
	promptStyle := style.NewStyle().Foreground(activeTheme.Primary).Bold(true)
	promptStr := promptStyle.Render(promptPrefix)

	p = StylePrompt(p, promptPrefix, promptStr)

	if bytes.Contains(p, []byte("\x1b[2J")) {
		if ki.OnRedrawScreen != nil {
			ki.OnRedrawScreen()
		}
		cleaned := bytes.ReplaceAll(p, []byte("\x1b[H\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[2J\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[2J"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[H"), nil)
		cleaned = bytes.ReplaceAll(cleaned, []byte("\x1b[1;1H"), nil)
		var err error
		if len(cleaned) > 0 {
			_, err = ki.writeToTerminal(cleaned)
		}
		if err != nil {
			return 0, err
		}
		return len(p), nil
	}

	if bytes.Contains(p, []byte("\x1b[H")) || bytes.Contains(p, []byte("\x1b[1;1H")) {
		return len(p), nil
	}

	if ki.IsAtMainPrompt && ki.RL != nil {
		var targetWriter io.Writer
		if ki.W != nil {
			targetWriter = ki.W
		} else if w, ok := ki.R.(io.Writer); ok {
			targetWriter = w
		} else {
			targetWriter = os.Stderr
		}
		if ki.OnDrawStaticControls != nil {
			ki.OnDrawStaticControls(targetWriter)
		}
		return len(p), nil
	}

	return ki.writeToTerminal(p)
}

func (ki *KeyInterceptorReader) writeToTerminal(p []byte) (int, error) {
	if ki.W != nil {
		return ki.W.Write(p)
	}
	if w, ok := ki.R.(io.Writer); ok {
		return w.Write(p)
	}
	return os.Stdout.Write(p)
}

func (ki *KeyInterceptorReader) GetInputChan() chan byte {
	return ki.InputChan
}

func (ki *KeyInterceptorReader) Read(p []byte) (int, error) {
	if len(ki.PasteRemaining) > 0 {
		n := copy(p, ki.PasteRemaining)
		ki.PasteRemaining = ki.PasteRemaining[n:]
		return n, nil
	}

	var n int
	var err error
	if ki.InputChan == nil {
		if ki.R == nil {
			return 0, io.EOF
		}
		n, err = ki.R.Read(p)
		if err != nil {
			return n, err
		}
	} else {
		var b byte
		select {
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

		if b != 4 {
			if ki.OnClearPromptHint != nil {
				ki.OnClearPromptHint()
			}
		}

		p[0] = b
		n = 1

		for n < len(p) {
			select {
			case injected, ok := <-ki.InjectChan:
				if ok {
					p[n] = injected
					n++
					continue
				}
			default:
			}
			break
		}

		if n == 1 && ki.InputChan != nil {
			select {
			case input, ok := <-ki.InputChan:
				if ok {
					p[n] = input
					n++
				}
			case <-time.After(3 * time.Millisecond):
			}
		}

		for n < len(p) {
			hasEscape := false
			for i := 0; i < n; i++ {
				if p[i] == 27 {
					hasEscape = true
					break
				}
			}

			if hasEscape || n > 1 {
				select {
				case input, ok := <-ki.InputChan:
					if ok {
						p[n] = input
						n++
					} else {
						goto done
					}
				case <-time.After(15 * time.Millisecond):
					goto done
				}
			} else {
				select {
				case input, ok := <-ki.InputChan:
					if ok {
						p[n] = input
						n++
					} else {
						break
					}
				default:
					goto done
				}
			}
		}
	done:
	}

	n, _ = ki.handlePasteData(p, n)

	writeIdx := 0
	for i := 0; i < n; i++ {
		b := p[i]
		if b == 3 { // Ctrl+C
			if ki.Agent != nil {
				ki.Agent.TasksMu.Lock()
				if ki.Agent.StreamingTask != "" {
					streamingID := ki.Agent.StreamingTask
					ki.Agent.StreamingTask = ""
					ki.Agent.TasksMu.Unlock()
					fmt.Fprintf(os.Stderr, "\n[stopped streaming %s]\n", streamingID)
					if ki.IsAtMainPrompt {
						ki.CtrlCInterrupted = true
						p[writeIdx] = '\n'
						writeIdx++
					}
					continue
				}
				ki.Agent.TasksMu.Unlock()
			}
			if ki.IsAtMainPrompt {
				ki.CtrlCInterrupted = true
				p[writeIdx] = '\n'
				writeIdx++
			} else {
				p[writeIdx] = b
				writeIdx++
			}
		} else if b == 4 { // Ctrl+D
			p[writeIdx] = b
			writeIdx++
		} else if b == 20 || b == 18 || b == 15 { // Ctrl+T, Ctrl+R, or Ctrl+O
			if b == 20 { // Ctrl+T
				ki.handleCtrlT()
			} else if b == 18 { // Ctrl+R
				ki.handleCtrlR()
			} else if b == 15 { // Ctrl+O
				ki.handleCtrlO()
			}
		} else {
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}
