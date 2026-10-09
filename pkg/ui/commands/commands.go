package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// HandleSlashCommand processes slash commands from the REPL.
// It returns (handled, quit).
func HandleSlashCommand(
	a *agent.Agent,
	line string,
	messages *[]db.Message,
	allowedTools []string,
	theme *style.UITheme,
	w io.Writer,
	currentSessionID *string,
	rlHistory term.History,
	mam *swarm.MultiAgentManager,
	kiReader *interceptor.KeyInterceptorReader,
) (bool, bool) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	isHelp := lower == "help" || lower == "h" || lower == "?" || lower == "/help" || lower == "/commands" || lower == "/h" || lower == "/?" || strings.HasPrefix(lower, "help ") || strings.HasPrefix(lower, "/help ")
	if !strings.HasPrefix(trimmed, "/") && !isHelp {
		return false, false
	}

	parts := strings.Fields(line)
	cmdName := parts[0]

	calcHistoryTokens := func() (int, int, bool) {
		var msgs []db.Message
		if messages != nil {
			msgs = *messages
		}
		return interceptor.CalculateActiveTokenUsage(a, msgs, allowedTools, mam)
	}

	switch cmdName {
	case "/exit", "/quit":
		if a != nil {
			a.KillAllTasks()
		}
		return true, true
	case "/toggle", "/collapse", "/expand":
		if cmdName == "/collapse" {
			a.Config.CollapseResults = true
		} else if cmdName == "/expand" {
			a.Config.CollapseResults = false
		} else {
			a.Config.CollapseResults = !a.Config.CollapseResults
		}
		_ = config.SaveConfig(a.ConfigPath, a.Config)
		ui.SetCollapseStatus(a.Config.CollapseResults)
		if kiReader != nil {
			ui.RedrawScreen(w, a, kiReader, kiReader.RL)
		} else {
			ui.RedrawScreen(w, a, nil, nil)
		}
		return true, false
	case "/queue", "/queues":
		if kiReader == nil {
			fmt.Fprintln(w, "queue is not available.")
			return true, false
		}
		if len(parts) > 1 && parts[1] == "clear" {
			n := kiReader.ClearQueue()
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = 0
			ui.GetUI().StateMu.Unlock()
			ui.DrawStatusBar(os.Stderr, *theme)
			fmt.Fprintf(w, "cleared %d queued prompt(s).\n", n)
			return true, false
		}
		prompts := kiReader.GetQueuedPrompts()
		if len(prompts) == 0 {
			fmt.Fprintln(w, "prompt queue is empty. (type and press Enter during generation to queue prompts)")
			return true, false
		}
		fmt.Fprintf(w, "prompt queue (%d item(s)):\n", len(prompts))
		for i, p := range prompts {
			fmt.Fprintf(w, "  %d. %s\n", i+1, p)
		}
		return true, false
	case "/task", "/tasks":
		handleTaskCommand(a, parts, theme, w, kiReader)
		return true, false
	case "/help", "/h", "/commands", "?", "/?", "help", "h":
		render.RenderHelp(w, *theme)
		return true, false
	case "/config", "/set":
		handleConfigCommand(a, cmdName, parts, messages, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/provider", "/providers", "/p":
		ui.HandleProviderCommand(a, parts, messages, *theme, w, kiReader)
		return true, false
	case "/skills", "/skill":
		handleSkillsCommand(a, parts, messages, currentSessionID, theme, w)
		return true, false
	case "/rewind", "/clear", "/clean", "/reset":
		handleClearCommand(a, messages, currentSessionID, rlHistory, kiReader, w)
		return true, false
	case "/stats", "/tokens", "/token", "/usage":
		handleStatsCommand(a, messages, theme, w)
		return true, false
	case "/session", "/sessions":
		handleSessionCommand(a, parts, messages, currentSessionID, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/compress":
		handleCompressCommand(a, messages, currentSessionID, theme, w, calcHistoryTokens)
		return true, false
	case "/context", "/ctx":
		handleContextCommand(a, parts, messages, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/debug":
		handleDebugCommand(a, w)
		return true, false
	case "/mcp", "/mcps":
		ui.HandleMCPCommand(a, parts, messages, *theme, w, kiReader)
		return true, false
	case "/agent", "/agents":
		handleAgentCommand(a, line, parts, mam, theme, w, kiReader)
		return true, false
	case "/reload":
		handleReloadCommand(a, w)
		return true, false
	case "/plugins", "/plugin":
		handlePluginsCommand(a, theme, w)
		return true, false
	case "/extensions", "/extension":
		handleExtensionsCommand(a, theme, w)
		return true, false
	default:
		handledExt, errExt := ui.RunExtension(a, cmdName, parts[1:], messages, w)
		if handledExt {
			if errExt != nil {
				fmt.Fprintf(w, "extension error: %v\n", errExt)
			}
			return true, false
		}
		fmt.Fprintf(w, "unknown slash command: %s. type /help or ? for commands list.\n", cmdName)
		return true, false
	}
}

func getActiveTasks(a *agent.Agent) int {
	return ui.GetActiveTasks(a)
}
