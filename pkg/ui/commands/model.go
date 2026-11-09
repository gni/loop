package commands

import (
	"fmt"
	"io"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/ui"
	"loop/pkg/ui/style"
)

func handleModelCommand(
	a *agent.Agent,
	parts []string,
	theme *style.UITheme,
	w io.Writer,
) {
	next, err := a.Configuration()
	if err != nil {
		fmt.Fprintln(w, err)
		return
	}

	if len(parts) < 2 {
		providerName := next.ActiveProvider
		if providerName == "" {
			providerName = "default"
		}
		modelName := next.Model
		if modelName == "" {
			modelName = "not set"
		}
		limit := next.ContextWindowLimit
		fmt.Fprintf(w, "Current model: %s (provider: %s, context limit: %d tokens)\n", modelName, providerName, limit)
		fmt.Fprintln(w, "Usage: /model <model_name>")
		return
	}

	newModel := strings.TrimSpace(strings.Join(parts[1:], " "))
	if newModel == "" {
		fmt.Fprintln(w, "usage: /model <model_name>")
		return
	}

	next.Model = newModel
	next.UpdateActiveProvider()
	if err := a.UpdateConfig(next); err != nil {
		fmt.Fprintf(w, "model update: %v\n", err)
		if !agent.ConfigApplied(err) {
			return
		}
	}
	if a.ConfigPath != "" {
		saveConfigUpdate(a, w)
	}

	if a.UI != nil {
		ui.UpdateStatus(a.Config.Model, -1, -1, 0, a.Config.ContextWindowLimit, false, 0, a.CountActiveTasks(), a.Config.ShowTokens)
	}
	if theme != nil {
		ui.DrawStatusBar(w, *theme)
	}

	providerName := a.Config.ActiveProvider
	if providerName != "" {
		fmt.Fprintf(w, "Updated model for provider '%s' to '%s'.\n", providerName, newModel)
	} else {
		fmt.Fprintf(w, "Updated model to '%s'.\n", newModel)
	}
}
