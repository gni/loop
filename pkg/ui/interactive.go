package ui

import (
	"io"

	"loop/pkg/agent"
	"loop/pkg/ui/interactive"
)

func init() {
	interactive.SetActiveReaderProvider(func() io.Reader {
		ui := getUI()
		if ui != nil {
			ui.StateMu.Lock()
			defer ui.StateMu.Unlock()
			return ui.ActiveInputReader
		}
		return nil
	})
	interactive.OnApprovalPromptChange(func(in bool) {
		ui := getUI()
		if ui != nil {
			ui.StateMu.Lock()
			ui.InApprovalPrompt = in
			ui.StateMu.Unlock()
		}
	})
}

func getInteractiveIO(kiReader io.Reader) (io.Reader, io.Writer, func()) {
	return interactive.GetInteractiveIO(kiReader)
}

func AskForApproval(w io.Writer, theme UITheme) (bool, bool) {
	return interactive.AskForApproval(w, theme)
}

func AskUserQuestion(w io.Writer, theme UITheme, question string, options []string, recommended string) (string, error) {
	return interactive.AskUserQuestion(w, theme, question, options, recommended)
}

func AskForSubagentCancellation(w io.Writer, theme UITheme, agentName string) agent.SubagentCancellationDecision {
	return interactive.AskForSubagentCancellation(w, theme, agentName)
}

var RunInteractiveConfig = interactive.RunInteractiveConfig
var RunInteractiveProviderConfig = interactive.RunInteractiveProviderConfig
var RunInteractiveMCPConfig = interactive.RunInteractiveMCPConfig
