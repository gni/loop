package interceptor

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/terminal"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

var promptNavigationKeyReplacer = strings.NewReplacer(
	"\x1b[1;5D", "\x1b[1;3D",
	"\x1b[1;5C", "\x1b[1;3C",
	"\x1b[5D", "\x1b[1;3D",
	"\x1b[5C", "\x1b[1;3C",
	"\x1bO1;5D", "\x1b[1;3D",
	"\x1bO1;5C", "\x1b[1;3C",
)

func NormalizePromptNavigationKeys(input []byte) []byte {
	if !bytes.ContainsRune(input, '\x1b') {
		return input
	}
	return []byte(promptNavigationKeyReplacer.Replace(string(input)))
}

func (ki *KeyInterceptorReader) ResetTypeAheadLocked() {
	ki.TypeAheadBuffer = nil
	ki.TypeAheadPastes = nil
	ki.HistoryIndex = -1
	ki.SavedTypeAhead = nil
}

func (ki *KeyInterceptorReader) NavigateHistory(direction int) {
	if ki.Hist == nil && ki.RL != nil && ki.RL.History != nil {
		ki.Hist = ki.RL.History
	}
	if ki.Hist == nil || ki.Hist.Len() == 0 {
		return
	}

	if direction > 0 {
		if ki.HistoryIndex == -1 {
			ki.SavedTypeAhead = append([]byte(nil), ki.TypeAheadBuffer...)
			ki.HistoryIndex = 0
			ki.TypeAheadBuffer = []byte(ki.Hist.At(ki.HistoryIndex))
		} else if ki.HistoryIndex+1 < ki.Hist.Len() {
			ki.HistoryIndex++
			ki.TypeAheadBuffer = []byte(ki.Hist.At(ki.HistoryIndex))
		}
	} else if direction < 0 {
		if ki.HistoryIndex > 0 {
			ki.HistoryIndex--
			ki.TypeAheadBuffer = []byte(ki.Hist.At(ki.HistoryIndex))
		} else if ki.HistoryIndex == 0 {
			ki.HistoryIndex = -1
			ki.TypeAheadBuffer = append([]byte(nil), ki.SavedTypeAhead...)
			ki.SavedTypeAhead = nil
		}
	}

	if history, ok := ki.Hist.(interface{ GetFull(string) string }); ok {
		ki.TypeAheadBuffer = []byte(history.GetFull(string(ki.TypeAheadBuffer)))
	}
	ki.RedrawTypeAhead()
}

func (ki *KeyInterceptorReader) HandleCtrlO() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.TasksMu.Lock()
	targetTaskId := ki.Agent.StreamingTask
	ki.Agent.TasksMu.Unlock()
	if targetTaskId == "" {
		targetTaskId = ki.Agent.GetLastRunningTaskId()
	}
	if targetTaskId != "" {
		dest := ki.W
		if chatWriter := ki.Agent.GetChatWriter(); chatWriter != nil && chatWriter != os.Stderr {
			dest = chatWriter
		}
		ki.Agent.ToggleStreaming(targetTaskId, dest)
		return
	}

	// Config is also read by the stream renderer. Apply active changes on that
	// same foreground path, preserving the parity of rapid repeated toggles.
	if ki.ActiveTurn.Load() {
		ki.PendingCollapseToggles.Add(1)
		return
	}
	ki.toggleCollapse()

	if ki.OnRedrawScreen != nil {
		ki.OnRedrawScreen()
	}
}

func (ki *KeyInterceptorReader) saveAgentConfigSnapshot() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	err := ki.Agent.SaveConfig()
	if err != nil {
		output := ki.W
		if output == nil {
			output = os.Stderr
		}
		fmt.Fprintf(output, "\nview updated in memory, but settings could not be saved: %v\n", err)
	}
}

func (ki *KeyInterceptorReader) toggleCollapse() {
	var collapsed bool
	if !ki.updateLiveSettings(func(next *agent.LiveSettings) {
		next.CollapseResults = !next.CollapseResults
		collapsed = next.CollapseResults
	}) {
		return
	}
	if ki.Agent.UI != nil {
		ki.Agent.UI.SetCollapseStatus(collapsed)
	}
}

// ApplyPendingCollapse runs on the foreground stream consumer or REPL.
func (ki *KeyInterceptorReader) ApplyPendingCollapse() bool {
	presses := ki.PendingCollapseToggles.Swap(0)
	if presses%2 == 0 {
		return false
	}
	ki.toggleCollapse()
	return true
}

func (ki *KeyInterceptorReader) HandleCtrlT() {
	if !ki.updateLiveSettings(func(next *agent.LiveSettings) { next.ShowThinking = !next.ShowThinking }) {
		return
	}
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlG() {
	if !ki.updateLiveSettings(func(next *agent.LiveSettings) { next.GuidedMode = !next.GuidedMode }) {
		return
	}
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlR() {
	if !ki.updateLiveSettings(func(next *agent.LiveSettings) {
		switch strings.ToLower(strings.TrimSpace(next.ReasoningEffort)) {
		case "low":
			next.ReasoningEffort = "medium"
		case "medium":
			next.ReasoningEffort = "high"
		case "high":
			next.ReasoningEffort = "max"
		case "max":
			next.ReasoningEffort = "off"
		default:
			next.ReasoningEffort = "low"
		}
	}) {
		return
	}
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) updateLiveSettings(change func(*agent.LiveSettings)) bool {
	if ki.Agent == nil {
		return false
	}
	if err := ki.Agent.UpdateLiveSettings(change); err != nil {
		if ki.W != nil {
			fmt.Fprintf(ki.W, "settings update failed: %v\n", err)
		}
		return false
	}
	ki.saveAgentConfigSnapshot()
	return true
}

func (ki *KeyInterceptorReader) redrawPromptSeparatorOrFallback() {
	if ki.OnRedrawPromptSeparator != nil {
		ki.OnRedrawPromptSeparator()
	} else if ki.OnRedrawScreen != nil {
		ki.OnRedrawScreen()
	} else if ki.W != nil {
		ki.Agent.LLMProviderMu.Lock()
		themeName := ki.Agent.Config.Theme
		syntaxTheme := ki.Agent.Config.SyntaxTheme
		showThinking := ki.Agent.Config.ShowThinking
		effort := ki.Agent.Config.ReasoningEffort
		ki.Agent.LLMProviderMu.Unlock()
		theme := style.ResolveConfiguredTheme(themeName, syntaxTheme)
		width, _ := terminal.GetDimensions()
		fmt.Fprint(ki.W, render.FormatPromptSeparator(showThinking, effort, theme, width))
	}
}
