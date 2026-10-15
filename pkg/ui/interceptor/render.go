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