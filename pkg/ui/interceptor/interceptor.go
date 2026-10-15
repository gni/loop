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
	"loop/pkg/domain/limits"
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

	OnClearPromptHint       func()
	OnRedrawScreen          func()
	OnDrawStaticControls    func(w io.Writer)
	OnRedrawPromptSeparator func()
	OnClearCancelFunc       func()
}

func (ki *KeyInterceptorReader) copyReadData(p, data []byte) int {
	n := copy(p, data)
	if n < len(data) {
		ki.PasteRemaining = append([]byte(nil), data[n:]...)
	}
	return n
}

func (ki *KeyInterceptorReader) Drain() {
	for ki.InputChan != nil {
		select {
		case <-ki.InputChan:
		default:
			goto drainInject
		}
	}
drainInject:
	for ki.InjectChan != nil {
		select {
		case <-ki.InjectChan:
		default:
			return
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

	if ki.IsAtMainPrompt && ki.RL != nil {
		targetWriter := ki.W
		if targetWriter == nil {
			if w, ok := ki.R.(io.Writer); ok {
				targetWriter = w
			} else {
				targetWriter = os.Stderr
			}
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
	if ki.InputChan == nil {
		if ki.R == nil {
			return 0, io.EOF
		}
		var err error
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

		if b != 4 && ki.OnClearPromptHint != nil {
			ki.OnClearPromptHint()
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

		if n == 1 {
			select {
			case input, ok := <-ki.InputChan:
				if !ok {
					goto done
				}
				p[n] = input
				n++
			case <-time.After(3 * time.Millisecond):
			}
		}

		for n < len(p) {
			if bytes.IndexByte(p[:n], 27) >= 0 {
				select {
				case input, ok := <-ki.InputChan:
					if !ok {
						goto done
					}
					p[n] = input
					n++
				case <-time.After(limits.EscapeDisambiguation()):
					goto done
				}
			} else {
				select {
				case input, ok := <-ki.InputChan:
					if !ok {
						goto done
					}
					p[n] = input
					n++
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
		default:
			p[writeIdx] = b
			writeIdx++
		}
	}

	return writeIdx, nil
}