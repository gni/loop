package interceptor

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"unicode/utf8"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

var defaultTerminalMu sync.Mutex

type CRNLWriter struct {
	W io.Writer
}

func (cw CRNLWriter) Write(p []byte) (int, error) {
	var buf []byte
	for i := 0; i < len(p); i++ {
		if p[i] == '\n' && (i == 0 || p[i-1] != '\r') {
			buf = append(buf, '\r')
		}
		buf = append(buf, p[i])
	}
	if _, err := cw.W.Write(buf); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (cw CRNLWriter) Unwrap() io.Writer {
	return cw.W
}

// SafeShellOutputWriter preserves shell text and SGR colors while discarding
// terminal commands that could move the cursor, erase chat, or change margins.
// Each command pipe gets its own instance to handle split escape sequences.
type SafeShellOutputWriter struct {
	w     io.Writer
	state uint8
	csi   []byte
}

func NewSafeShellOutputWriter(w io.Writer) *SafeShellOutputWriter {
	return &SafeShellOutputWriter{w: w}
}

func (s *SafeShellOutputWriter) Write(data []byte) (int, error) {
	var out bytes.Buffer
	for _, b := range data {
		switch s.state {
		case 0:
			switch {
			case b == 0x1b:
				s.state = 1
			case b == '\n' || b == '\r' || b == '\t' || b >= 0x20:
				out.WriteByte(b)
			}
		case 1:
			switch b {
			case '[':
				s.state = 2
				s.csi = s.csi[:0]
			case ']', 'P', '_', '^', 'X':
				s.state = 3
			default:
				s.state = 0
			}
		case 2:
			if b >= 0x40 && b <= 0x7e {
				if b == 'm' && len(s.csi) < 64 {
					out.WriteString("\x1b[")
					out.Write(s.csi)
					out.WriteByte('m')
				}
				s.state = 0
			} else if (b >= '0' && b <= '9') || b == ';' || b == ':' {
				if len(s.csi) < 64 {
					s.csi = append(s.csi, b)
				} else {
					s.csi = s.csi[:64]
				}
			} else {
				s.state = 4
			}
		case 3:
			if b == 0x07 {
				s.state = 0
			} else if b == 0x1b {
				s.state = 5
			}
		case 4:
			if b >= 0x40 && b <= 0x7e {
				s.state = 0
			}
		case 5:
			if b == '\\' || b == 0x07 {
				s.state = 0
			} else if b != 0x1b {
				s.state = 3
			}
		}
	}
	if out.Len() > 0 {
		if _, err := s.w.Write(out.Bytes()); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

func ActiveToolAllowlist(reader *KeyInterceptorReader) []string {
	if reader == nil {
		return nil
	}
	return reader.AllowedTools
}

func CalculateActiveTokenUsage(
	a *agent.Agent,
	messages []db.Message,
	allowedTools []string,
	mam *swarm.MultiAgentManager,
) (int, int, bool) {
	activeMessages := messages
	if mam != nil && mam.ActiveAgent != nil {
		mam.ActiveAgent.HistoryMu.RLock()
		activeMessages = append([]db.Message(nil), mam.ActiveAgent.History...)
		mam.ActiveAgent.HistoryMu.RUnlock()
	}
	pTok, _, est := a.GetGlobalTokenUsage(activeMessages, allowedTools)
	globalOut := a.GetSessionTotalCompletionTokens(activeMessages)
	if mam != nil && mam.ActiveAgent == nil {
		globalOut += mam.GetSubagentsCompletionTokens()
	}
	return pTok, globalOut, est
}

func StylePrompt(p []byte, prefix string, styledPrefix string) []byte {
	for _, lead := range []string{"", "\r", "\r\x1b[K", "\r\x1b[2K"} {
		full := []byte(lead + prefix)
		if bytes.HasPrefix(p, full) {
			return append([]byte(lead+styledPrefix), p[len(full):]...)
		}
	}
	return p
}

func (ki *KeyInterceptorReader) RedrawTypeAhead() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	activeTheme := style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
	promptPrefix := "> "
	if ki.MAM != nil && ki.MAM.ActiveAgent != nil {
		promptPrefix = fmt.Sprintf("[%s]%s", ki.MAM.ActiveAgent.Name, promptPrefix)
	}

	promptStr, fullPrefixPlain := style.FormatPromptWithQueue(activeTheme, promptPrefix, ki.QueueLen())

	termW, height := terminal.GetDimensions()
	if height <= 0 {
		return
	}

	prefixLen := utf8.RuneCountInString(style.StripAnsi(fullPrefixPlain))
	availableWidth := termW - prefixLen - 1
	if availableWidth < 1 {
		availableWidth = 1
	}

	inputRunes := []rune(string(ki.TypeAheadBuffer))
	if len(inputRunes) > availableWidth {
		inputRunes = inputRunes[len(inputRunes)-availableWidth:]
	}
	displayInput := string(inputRunes)
	cursorCol := prefixLen + len(inputRunes) + 1
	if termW > 0 && cursorCol > termW {
		cursorCol = termW
	}

	if ki.Agent.CurrentWriter() != nil {
		if writer, ok := ki.Agent.CurrentWriter().(interface{ SetPromptCol(int) }); ok {
			writer.SetPromptCol(cursorCol)
		}
	}

	promptRow := height - 2 - ki.PasteLinesOffset
	if promptRow < 1 {
		promptRow = 1
	}

	defaultTerminalMu.Lock()
	defer defaultTerminalMu.Unlock()
	if ki.W != nil {
		fmt.Fprintf(ki.W, "\x1b[%d;1H\x1b[2K%s%s\x1b[%d;%dH", promptRow, promptStr, displayInput, promptRow, cursorCol)
	}
}
