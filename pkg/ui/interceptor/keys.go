package interceptor

import (
	"bytes"
	"fmt"
	"strings"

	"loop/pkg/config"
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

// NormalizePromptNavigationKeys standardizes terminal escape sequences for word navigation.
func NormalizePromptNavigationKeys(input []byte) []byte {
	return normalizePromptNavigationKeys(input)
}

func normalizePromptNavigationKeys(input []byte) []byte {
	if !bytes.ContainsRune(input, '\x1b') {
		return input
	}
	return []byte(promptNavigationKeyReplacer.Replace(string(input)))
}

func (ki *KeyInterceptorReader) ResetTypeAheadLocked() {
	ki.resetTypeAheadLocked()
}

func (ki *KeyInterceptorReader) resetTypeAheadLocked() {
	ki.TypeAheadBuffer = nil
	ki.HistoryIndex = -1
	ki.SavedTypeAhead = nil
}

func (ki *KeyInterceptorReader) NavigateHistory(direction int) {
	ki.navigateHistory(direction)
}

func (ki *KeyInterceptorReader) navigateHistory(direction int) {
	if ki.Hist == nil && ki.RL != nil && ki.RL.History != nil {
		ki.Hist = ki.RL.History
	}
	if ki.Hist == nil || ki.Hist.Len() == 0 {
		return
	}

	if direction > 0 { // Up arrow / older history
		if ki.HistoryIndex == -1 {
			ki.SavedTypeAhead = append([]byte(nil), ki.TypeAheadBuffer...)
			ki.HistoryIndex = 0
			entry := ki.Hist.At(ki.HistoryIndex)
			ki.TypeAheadBuffer = []byte(entry)
		} else if ki.HistoryIndex+1 < ki.Hist.Len() {
			ki.HistoryIndex++
			entry := ki.Hist.At(ki.HistoryIndex)
			ki.TypeAheadBuffer = []byte(entry)
		}
	} else if direction < 0 { // Down arrow / newer history
		if ki.HistoryIndex > 0 {
			ki.HistoryIndex--
			entry := ki.Hist.At(ki.HistoryIndex)
			ki.TypeAheadBuffer = []byte(entry)
		} else if ki.HistoryIndex == 0 {
			ki.HistoryIndex = -1
			ki.TypeAheadBuffer = append([]byte(nil), ki.SavedTypeAhead...)
			ki.SavedTypeAhead = nil
		}
	}

	ki.RedrawTypeAhead()
}

func (ki *KeyInterceptorReader) HandleCtrlO() {
	ki.handleCtrlO()
}

func (ki *KeyInterceptorReader) handleCtrlO() {
	if ki.Agent == nil {
		return
	}
	runningTaskId := ki.Agent.GetLastRunningTaskId()
	if runningTaskId != "" {
		ki.Agent.ToggleStreaming(runningTaskId, ki.W)
	} else {
		ki.Agent.Config.CollapseResults = !ki.Agent.Config.CollapseResults
		_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
		if ki.Agent.UI != nil {
			ki.Agent.UI.SetCollapseStatus(ki.Agent.Config.CollapseResults)
		}

		if ki.OnRedrawScreen != nil {
			ki.OnRedrawScreen()
		}

		if ki.Agent.CurrentWriter != nil {
			if fr, ok := ki.Agent.CurrentWriter.(interface{ ForceReposition() }); ok {
				fr.ForceReposition()
			}
		}
	}
}

func (ki *KeyInterceptorReader) HandleCtrlT() {
	ki.handleCtrlT()
}

func (ki *KeyInterceptorReader) handleCtrlT() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	activeTheme := style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
	ki.Agent.Config.ShowThinking = !ki.Agent.Config.ShowThinking
	if ki.Agent.Config.ShowThinking {
		if strings.ToLower(ki.Agent.Config.ReasoningEffort) == "off" || ki.Agent.Config.ReasoningEffort == "" {
			ki.Agent.Config.ReasoningEffort = "low"
		}
	}
	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	ki.redrawPromptSeparatorOrFallback(activeTheme)
}

func (ki *KeyInterceptorReader) HandleCtrlR() {
	ki.handleCtrlR()
}

func (ki *KeyInterceptorReader) handleCtrlR() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	activeTheme := style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
	currentEffort := strings.ToLower(strings.TrimSpace(ki.Agent.Config.ReasoningEffort))

	nextEffort := "low"
	switch currentEffort {
	case "off", "none", "":
		nextEffort = "low"
		ki.Agent.Config.ShowThinking = true
	case "low":
		nextEffort = "medium"
		ki.Agent.Config.ShowThinking = true
	case "medium":
		nextEffort = "high"
		ki.Agent.Config.ShowThinking = true
	case "high":
		nextEffort = "max"
		ki.Agent.Config.ShowThinking = true
	case "max":
		nextEffort = "off"
		ki.Agent.Config.ShowThinking = false
	default:
		nextEffort = "low"
		ki.Agent.Config.ShowThinking = true
	}

	ki.Agent.Config.ReasoningEffort = nextEffort
	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	ki.redrawPromptSeparatorOrFallback(activeTheme)
}

func (ki *KeyInterceptorReader) redrawPromptSeparatorOrFallback(theme style.UITheme) {
	if ki.OnRedrawPromptSeparator != nil {
		ki.OnRedrawPromptSeparator()
	} else if ki.OnRedrawScreen != nil {
		ki.OnRedrawScreen()
	} else if ki.W != nil {
		width, _ := terminal.GetDimensions()
		fmt.Fprint(ki.W, render.FormatPromptSeparator(ki.Agent.Config.ShowThinking, ki.Agent.Config.ReasoningEffort, theme, width))
	}
}
