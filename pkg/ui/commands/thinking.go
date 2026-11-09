package commands

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func handleThinkingCommand(a *agent.Agent, parts []string, w io.Writer) {
	next, err := a.Configuration()
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}

	currentEffort := strings.ToLower(strings.TrimSpace(next.ReasoningEffort))
	if currentEffort == "" {
		currentEffort = "none"
	}

	if len(parts) < 2 {
		fmt.Fprintf(w, "Current reasoning effort: %s (show thinking: %v)\nUsage: /thinking [off|low|medium|high|max]\n", currentEffort, next.ShowThinking)
		return
	}

	level, err := config.ParseReasoningEffort(parts[1])
	if err != nil {
		fmt.Fprintf(w, "%v\n", err)
		return
	}

	next.ReasoningEffort = level
	next.ShowThinking = level != "off"
	if err := a.UpdateConfig(next); err != nil {
		fmt.Fprintf(w, "reasoning update: %v\n", err)
		if !agent.ConfigApplied(err) {
			return
		}
	}

	saveConfigUpdate(a, w)
	fmt.Fprintf(w, "Reasoning effort set to: %s (show thinking: %v)\n", level, a.Config.ShowThinking)
}
