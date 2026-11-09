package ui

import (
	"io"
	"sync"

	"loop/pkg/agent"
	"loop/pkg/terminal"
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

func ForceExitAlternateScreen(w io.Writer) {
	terminal.Default().ForceExitAltScreen()
	if w != nil {
		terminal.New(w).ForceExitAltScreen()
	}
}

func MarkSessionReloaded() {
	ui := getUI()
	ui.StateMu.Lock()
	ui.SessionReloaded = true
	ui.StateMu.Unlock()
}

func ConsumeSessionReloaded() bool {
	ui := getUI()
	ui.StateMu.Lock()
	defer ui.StateMu.Unlock()
	reloaded := ui.SessionReloaded
	ui.SessionReloaded = false
	return reloaded
}

func GetActiveTasks(a *agent.Agent) int {
	if a == nil {
		return 0
	}
	return a.CountActiveTasks()
}
