package interceptor

import (
	"bytes"
	"fmt"
	"os"
	"strings"

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
	ki.Agent.LLMProviderMu.Lock()
	collapsed := !ki.Agent.Config.CollapseResults
	ki.Agent.Config.CollapseResults = collapsed
	ki.Agent.LLMProviderMu.Unlock()
	ki.saveAgentConfigSnapshot()
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
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.LLMProviderMu.Lock()
	ki.Agent.Config.ShowThinking = !ki.Agent.Config.ShowThinking
	ki.Agent.LLMProviderMu.Unlock()
	ki.saveAgentConfigSnapshot()
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlG() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.LLMProviderMu.Lock()
	ki.Agent.SetGuidedMode(!ki.Agent.IsGuidedMode())
	ki.Agent.LLMProviderMu.Unlock()
	ki.saveAgentConfigSnapshot()
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlR() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.LLMProviderMu.Lock()
	switch strings.ToLower(strings.TrimSpace(ki.Agent.Config.ReasoningEffort)) {
	case "low":
		ki.Agent.Config.ReasoningEffort = "medium"
	case "medium":
		ki.Agent.Config.ReasoningEffort = "high"
	case "high":
		ki.Agent.Config.ReasoningEffort = "max"
	case "max":
		ki.Agent.Config.ReasoningEffort = "off"
	default:
		ki.Agent.Config.ReasoningEffort = "low"
	}
	ki.Agent.LLMProviderMu.Unlock()

	ki.saveAgentConfigSnapshot()
	ki.redrawPromptSeparatorOrFallback()
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
