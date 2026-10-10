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
		if p[i] == '\n' {
			if i == 0 || p[i-1] != '\r' {
				buf = append(buf, '\r')
			}
		}
		buf = append(buf, p[i])
	}
	_, err := cw.W.Write(buf)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (cw CRNLWriter) Unwrap() io.Writer {
	return cw.W
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
	if bytes.HasPrefix(p, []byte(prefix)) {
		return append([]byte(styledPrefix), p[len(prefix):]...)
	}
	rPrefix := append([]byte{'\r'}, []byte(prefix)...)
	if bytes.HasPrefix(p, rPrefix) {
		return append(append([]byte{'\r'}, []byte(styledPrefix)...), p[len(rPrefix):]...)
	}
	rkPrefix := append([]byte("\r\x1b[K"), []byte(prefix)...)
	if bytes.HasPrefix(p, rkPrefix) {
		return append(append([]byte("\r\x1b[K"), []byte(styledPrefix)...), p[len(rkPrefix):]...)
	}
	r2kPrefix := append([]byte("\r\x1b[2K"), []byte(prefix)...)
	if bytes.HasPrefix(p, r2kPrefix) {
		return append(append([]byte("\r\x1b[2K"), []byte(styledPrefix)...), p[len(r2kPrefix):]...)
	}
	return p
}

func (ki *KeyInterceptorReader) RedrawTypeAhead() {
	ki.redrawTypeAhead()
}

func (ki *KeyInterceptorReader) redrawTypeAhead() {
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

	typeAheadCopy := string(ki.TypeAheadBuffer)
	pasteLinesOffset := ki.PasteLinesOffset

	prefixLen := utf8.RuneCountInString(style.StripAnsi(fullPrefixPlain))
	availableWidth := termW - prefixLen - 1
	if availableWidth < 1 {
		availableWidth = 1
	}

	inputRunes := []rune(typeAheadCopy)
	if len(inputRunes) > availableWidth {
		inputRunes = inputRunes[len(inputRunes)-availableWidth:]
	}
	displayInput := string(inputRunes)
	cursorCol := prefixLen + len(inputRunes) + 1
	if termW > 0 && cursorCol > termW {
		cursorCol = termW
	}

	if ki.Agent != nil && ki.Agent.CurrentWriter() != nil {
		if writer, ok := ki.Agent.CurrentWriter().(interface{ SetPromptCol(int) }); ok {
			writer.SetPromptCol(cursorCol)
		}
	}

	promptRow := height - 2 - pasteLinesOffset
	if promptRow < 1 {
		promptRow = 1
	}

	defaultTerminalMu.Lock()
	defer defaultTerminalMu.Unlock()
	if ki.W != nil {
		fmt.Fprintf(ki.W, "\x1b[%d;1H\x1b[2K%s%s\x1b[%d;%dH", promptRow, promptStr, displayInput, promptRow, cursorCol)
	}
}
