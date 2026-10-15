package interceptor

import (
	"bytes"
	"fmt"
	"os"
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

func NormalizePromptNavigationKeys(input []byte) []byte {
	if !bytes.ContainsRune(input, '\x1b') {
		return input
	}
	return []byte(promptNavigationKeyReplacer.Replace(string(input)))
}

func (ki *KeyInterceptorReader) ResetTypeAheadLocked() {
	ki.TypeAheadBuffer = nil
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

	ki.RedrawTypeAhead()
}

func (ki *KeyInterceptorReader) HandleCtrlO() {
	if ki.Agent == nil {
		return
	}
	targetTaskId := ki.Agent.StreamingTask
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

	ki.Agent.Config.CollapseResults = !ki.Agent.Config.CollapseResults
	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	if ki.Agent.UI != nil {
		ki.Agent.UI.SetCollapseStatus(ki.Agent.Config.CollapseResults)
	}
	if ki.OnRedrawScreen != nil {
		ki.OnRedrawScreen()
	}
	if fr, ok := ki.Agent.CurrentWriter().(interface{ ForceReposition() }); ok {
		fr.ForceReposition()
	}
}

func (ki *KeyInterceptorReader) HandleCtrlT() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.Config.ShowThinking = !ki.Agent.Config.ShowThinking
	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlG() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
	ki.Agent.SetGuidedMode(!ki.Agent.IsGuidedMode())
	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) HandleCtrlR() {
	if ki.Agent == nil || ki.Agent.Config == nil {
		return
	}
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

	_ = config.SaveConfig(ki.Agent.ConfigPath, ki.Agent.Config)
	ki.redrawPromptSeparatorOrFallback()
}

func (ki *KeyInterceptorReader) redrawPromptSeparatorOrFallback() {
	if ki.OnRedrawPromptSeparator != nil {
		ki.OnRedrawPromptSeparator()
	} else if ki.OnRedrawScreen != nil {
		ki.OnRedrawScreen()
	} else if ki.W != nil {
		theme := style.ResolveConfiguredTheme(ki.Agent.Config.Theme, ki.Agent.Config.SyntaxTheme)
		width, _ := terminal.GetDimensions()
		fmt.Fprint(ki.W, render.FormatPromptSeparator(ki.Agent.Config.ShowThinking, ki.Agent.Config.ReasoningEffort, theme, width))
	}
}