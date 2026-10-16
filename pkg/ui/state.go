package ui

import (
	"io"
	"sync"

	"loop/pkg/agent"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

var ActiveUI *AgentUIImpl

var fallbackUI = &AgentUIImpl{}

func getUI() *AgentUIImpl {
	if ActiveUI != nil {
		return ActiveUI
	}
	return fallbackUI
}

func GetUI() *AgentUIImpl {
	return getUI()
}

var TerminalMu sync.Mutex

var IsInteractive bool

func SetCollapseStatus(collapsed bool) {
	getUI().SetCollapseStatus(collapsed)
}

func SetScrollRegionOffset(offset int) {
	getUI().SetScrollRegionOffset(offset)
}

func InitStatusBar(w io.Writer) {
	getUI().InitStatusBar(w)
}

func ShutdownStatusBar(w io.Writer) {
	getUI().ShutdownStatusBar(w)
}

func StripAnsi(str string) string {
	return style.StripAnsi(str)
}

func stripAnsi(str string) string {
	return style.StripAnsi(str)
}

var (
	altTerms   = make(map[io.Writer]*terminal.Terminal)
	altTermsMu sync.Mutex
)

func termForWriter(w io.Writer) *terminal.Terminal {
	if w == nil {
		return terminal.Default()
	}
	altTermsMu.Lock()
	defer altTermsMu.Unlock()
	t, ok := altTerms[w]
	if !ok {
		t = terminal.New(w)
		altTerms[w] = t
	}
	return t
}

func EnterAlternateScreen(w io.Writer) {
	termForWriter(w).EnterAltScreen()
}

func ExitAlternateScreen(w io.Writer) {
	termForWriter(w).ExitAltScreen()
}

func ForceExitAlternateScreen(w io.Writer) {
	termForWriter(w).ForceExitAltScreen()
}

func GetActiveTasks(a *agent.Agent) int {
	if a == nil {
		return 0
	}
	return a.CountActiveTasks()
}