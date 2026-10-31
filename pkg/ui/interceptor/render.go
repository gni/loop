package interceptor

import (
	"bytes"
	"io"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/domain/output"
)

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

// SafeShellOutputWriter shares the terminal-control filter with the Bash tool.
type SafeShellOutputWriter = output.SafeShellOutputWriter

func NewSafeShellOutputWriter(w io.Writer) *SafeShellOutputWriter {
	return output.NewSafeShellOutputWriter(w)
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
	activeSubagent := false
	if mam != nil {
		if history, active := mam.ActiveHistorySnapshot(); active {
			activeMessages = history
			activeSubagent = true
		}
	}
	pTok, _, est := a.GetGlobalTokenUsage(activeMessages, allowedTools)
	globalOut := a.GetSessionTotalCompletionTokens(activeMessages)
	if mam != nil && !activeSubagent {
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
	if ki.OnRedrawTypeAhead != nil {
		ki.OnRedrawTypeAhead()
	}
}
