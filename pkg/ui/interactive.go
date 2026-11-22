package ui

import (
	"io"

	"loop/pkg/agent"
	"loop/pkg/ui/interactive"
	imcp "loop/pkg/ui/interactive/mcp"
	isettings "loop/pkg/ui/interactive/settings"
)

func init() {
	interactive.SetActiveReaderProvider(func() io.Reader {
		ui := getUI()
		if ui != nil {
			return ui.GetActiveInputReader()
		}
		return nil
	})
	interactive.OnApprovalPromptChange(func(in bool) {
		ui := getUI()
		if ui != nil {
			ui.StateMu.Lock()
			ui.InApprovalPrompt = in
			if !in {
				ui.LastStatusBarText = ""
			}
			ui.StateMu.Unlock()
		}
	})
	interactive.OnInteractiveMenuChange(func(in bool) {
		ui := getUI()
		if ui != nil {
			ui.StateMu.Lock()
			ui.InInteractiveMenu = in
			if !in {
				ui.LastStatusBarText = ""
			}
			ui.StateMu.Unlock()
		}
	})
}

func getInteractiveIO(kiReader io.Reader) (io.Reader, io.Writer, func()) {
	return interactive.GetInteractiveIO(kiReader)
}

func AskForApproval(w io.Writer, theme UITheme, allowSession bool) (bool, bool) {
	return interactive.AskForApproval(w, theme, allowSession)
}

func AskUserQuestion(w io.Writer, theme UITheme, question string, options []string, recommended string) (string, error) {
	return interactive.AskUserQuestion(w, theme, question, options, recommended)
}

func AskForSubagentCancellation(w io.Writer, theme UITheme, agentName string) agent.SubagentCancellationDecision {
	return interactive.AskForSubagentCancellation(w, theme, agentName)
}

var RunInteractiveConfig = isettings.RunInteractiveConfig
var RunInteractiveProviderConfig = interactive.RunInteractiveProviderConfig
var RunInteractiveMCPConfig = imcp.RunInteractiveMCPConfig
