package commands

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func handleThinkingCommand(a *agent.Agent, parts []string, w io.Writer) {
	if a == nil || a.Config == nil {
		fmt.Fprintln(w, "configuration unavailable.")
		return
	}

	currentEffort := strings.ToLower(strings.TrimSpace(a.Config.ReasoningEffort))
	if currentEffort == "" {
		currentEffort = "none"
	}

	if len(parts) < 2 {
		fmt.Fprintf(w, "Current reasoning effort: %s (show thinking: %v)\nUsage: /thinking [off|low|medium|high|max]\n", currentEffort, a.Config.ShowThinking)
		return
	}

	level, err := config.ParseReasoningEffort(parts[1])
	if err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return
	}

	a.Config.ReasoningEffort = level
	a.Config.ShowThinking = level != "off" && level != "none"

	saveConfigUpdate(a, w)
	fmt.Fprintf(w, "Reasoning effort set to: %s (show thinking: %v)\n", level, a.Config.ShowThinking)
}
