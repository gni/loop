package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func handleGuidedCommand(
	a *agent.Agent,
	parts []string,
	theme *style.UITheme,
	w io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
) {
	if a == nil {
		return
	}

	sub := "toggle"
	if len(parts) > 1 {
		sub = strings.ToLower(strings.TrimSpace(parts[1]))
	}

	switch sub {
	case "toggle":
		a.SetGuidedMode(!a.IsGuidedMode())
	case "on", "enable", "true", "1":
		a.SetGuidedMode(true)
	case "off", "disable", "false", "0":
		a.SetGuidedMode(false)
	case "approve":
		todos := a.GetTodos()
		if len(todos) == 0 {
			fmt.Fprintln(w, "no active todo plan to approve.")
			return
		}
		a.Guided.ApprovePlan(todos)
		fmt.Fprintf(w, "approved plan (%d step(s)).\n", len(todos))
		return
	case "plan", "status":
		var snaps []agent.FileSnapshot
		if a.Guided != nil {
			snaps = a.Guided.Snapshots()
		}
		fmt.Fprintf(w, "guided mode: %v (phase: %s, approved: %v, snapshots: %d)\n",
			a.IsGuidedMode(), a.Guided.Phase(), a.Guided.IsPlanApproved(), len(snaps))
		for i, item := range a.GetTodos() {
			fmt.Fprintf(w, "  %d. [%s] %s\n", i+1, item.Status, item.Task)
		}
		return
	default:
		fmt.Fprintln(w, "usage: /guided [on | off | status | approve]")
		return
	}

	if a.ConfigPath != "" && a.Config != nil {
		_ = config.SaveConfig(a.ConfigPath, a.Config)
	}
	stateStr := "disabled"
	if a.IsGuidedMode() {
		stateStr = "enabled"
	}
	fmt.Fprintf(w, "guided mode %s.\n", stateStr)
	ui.DrawStaticPromptSeparator(os.Stderr, a.Config.ShowThinking, a.Config.ReasoningEffort, *theme)
	ui.DrawStatusBar(os.Stderr, *theme)
	if kiReader != nil && kiReader.RL != nil {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, kiReader.RL)
	}
}

func handleRollbackCommand(a *agent.Agent, theme *style.UITheme, w io.Writer) {
	if a == nil || a.Guided == nil || len(a.Guided.Snapshots()) == 0 {
		fmt.Fprintln(w, "no modified files to roll back.")
		return
	}
	restored, err := a.RollbackChanges()
	if err != nil {
		fmt.Fprintf(w, "rollback error: %v\n", err)
	}
	for _, line := range restored {
		fmt.Fprintf(w, "  %s\n", line)
	}
}