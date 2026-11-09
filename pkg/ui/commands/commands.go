package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/diagnostics"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

// IsHelpCommand reports whether a trimmed user input line invokes the help reference.
func IsHelpCommand(trimmed string) bool {
	lower := strings.ToLower(strings.TrimSpace(trimmed))
	switch lower {
	case "help", "h", "?", "/help", "/commands", "/h", "/?":
		return true
	default:
		return strings.HasPrefix(lower, "help ") || strings.HasPrefix(lower, "/help ")
	}
}

func commandContext(a *agent.Agent, reader *interceptor.KeyInterceptorReader) context.Context {
	if reader != nil && reader.InputContext != nil {
		return reader.InputContext
	}
	if a != nil && a.Lifetime != nil {
		return a.Lifetime
	}
	return context.Background()
}

func terminalFromReader(kiReader *interceptor.KeyInterceptorReader) *term.Terminal {
	if kiReader != nil {
		return kiReader.RL
	}
	return nil
}

func syncCommandStatus(
	a *agent.Agent,
	messages *[]db.Message,
	theme style.UITheme,
	statusWriter io.Writer,
	kiReader *interceptor.KeyInterceptorReader,
	calcHistoryTokens func() (int, int, bool),
	refreshConsole bool,
) {
	if a == nil || a.Config == nil || calcHistoryTokens == nil {
		return
	}
	pTok, cTok, estimated := calcHistoryTokens()
	var msgs []db.Message
	if messages != nil {
		msgs = *messages
	}
	latestTurnTokens := a.GetLatestAssistantCompletionTokens(msgs)
	effLimit := a.GetEffectiveContextLimit(pTok)

	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatusBarText = ""
	ui.GetUI().StateMu.Unlock()

	ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, getActiveTasks(a), a.Config.ShowTokens, estimated)
	if refreshConsole {
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, terminalFromReader(kiReader))
	}
	ui.DrawStatusBar(statusWriter, theme)
}

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
	isHelp := IsHelpCommand(trimmed)
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
	case "/doctor":
		diagnostics.Run(commandContext(a, kiReader), a.Config, a.ConfigPath, a.WorkspaceRoot, len(parts) > 1 && (parts[1] == "offline" || parts[1] == "--offline")).Write(w)
		return true, false
	case "/changes":
		handleChangesCommand(commandContext(a, kiReader), a, len(parts) > 1 && parts[1] == "diff", w, *theme)
		return true, false

	case "/find", "/latest":
		history := ui.GetUI().ChatHistory
		if history == nil {
			fmt.Fprintln(w, "chat navigation is only available in an interactive conversation")
			return true, false
		}
		if cmdName == "/latest" || (len(parts) == 2 && parts[1] == "clear") {
			history.Latest()
			return true, false
		}
		if len(parts) < 2 {
			fmt.Fprintln(w, "usage: /find <text> | next | prev | clear")
			return true, false
		}
		query := strings.Join(parts[1:], " ")
		literal := parts[1] == "--"
		if literal {
			query = strings.Join(parts[2:], " ")
			if query == "" {
				fmt.Fprintln(w, "usage: /find -- <text>")
				return true, false
			}
		}
		direction := 0
		if !literal && query == "next" {
			query = ""
			direction = 1
		} else if !literal && query == "prev" {
			query = ""
			direction = -1
		}
		if err := history.Find(query, direction); err != nil {
			fmt.Fprintln(w, err)
		}
		return true, false
	case "/exit", "/quit":
		if a != nil {
			a.KillAllTasks()
		}
		return true, true
	case "/nostop", "/loop":
		handleNostopCommand(a, parts, theme, w, kiReader)
		return true, false
	case "/guided", "/guide":
		handleGuidedCommand(a, parts, theme, w, kiReader)
		return true, false
	case "/plan":
		handleGuidedCommand(a, []string{"/guided", "plan"}, theme, w, kiReader)
		return true, false
	case "/rollback":
		handleRollbackCommand(a, theme, w)
		return true, false
	case "/undo":
		handleUndoCommand(a, messages, currentSessionID, theme, w, kiReader)
		return true, false
	case "/tree":
		handleTreeCommand(a, parts, currentSessionID, theme, w)
		return true, false
	case "/branch":
		handleBranchCommand(a, parts, messages, currentSessionID, theme, w, kiReader)
		return true, false
	case "/fork", "/clone":
		handleForkCommand(a, parts, messages, currentSessionID, theme, w)
		return true, false
	case "/rename", "/name":
		if len(parts) < 2 {
			fmt.Fprintln(w, "usage: /rename <new_name>")
			return true, false
		}
		handleSessionCommand(a, append([]string{"/session", "rename"}, parts[1:]...), messages, currentSessionID, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/copy", "/clip":
		handleCopyCommand(messages, w)
		return true, false
	case "/export":
		handleExportCommand(a, parts, messages, currentSessionID, w)
		return true, false
	case "/thinking", "/reasoning":
		handleThinkingCommand(a, parts, w)
		return true, false
	case "/model":
		handleModelCommand(a, parts, theme, w)
		return true, false
	case "/new":
		handleSessionCommand(a, []string{"/session", "new"}, messages, currentSessionID, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/resume":
		handleSessionCommand(a, append([]string{"/session", "load"}, parts[1:]...), messages, currentSessionID, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/hotkeys", "/keys":
		render.RenderHotkeys(w, *theme)
		return true, false
	case "/toggle", "/collapse", "/expand":
		if err := a.UpdateLiveSettings(func(next *agent.LiveSettings) {
			switch cmdName {
			case "/collapse":
				next.CollapseResults = true
			case "/expand":
				next.CollapseResults = false
			default:
				next.CollapseResults = !next.CollapseResults
			}
		}); err != nil {
			fmt.Fprintln(w, err)
			return true, false
		}
		saveConfigUpdate(a, w)
		ui.SetCollapseStatus(a.Config.CollapseResults)
		ui.RedrawScreen(w, a, kiReader, terminalFromReader(kiReader))
		return true, false
	case "/queue", "/queues":
		if kiReader == nil {
			fmt.Fprintln(w, "queue is not available.")
			return true, false
		}
		if len(parts) > 1 {
			if notice, handled := kiReader.ControlQueue(parts[1]); handled {
				fmt.Fprintln(w, notice)
				return true, false
			}
		}
		prompts := kiReader.GetQueuedPrompts()
		if len(prompts) == 0 {
			fmt.Fprintln(w, "prompt queue is empty. (type and press Enter during generation to queue prompts)")
			return true, false
		}
		fmt.Fprintf(w, "prompt queue (%d item(s)):\n", len(prompts))
		for i, p := range prompts {
			fmt.Fprintf(w, "  %d. %s\n", i+1, p.Text)
		}
		return true, false
	case "/task", "/tasks":
		if cmdName == "/tasks" && len(parts) == 1 {
			handleTaskCommand(a, []string{"/task", "list"}, theme, w, kiReader)
		} else {
			handleTaskCommand(a, parts, theme, w, kiReader)
		}
		return true, false
	case "/stop_task", "/task_kill", "/taskkill", "/kill", "/stop", "/task_stop", "/taskstop":
		handleTaskCommand(a, append([]string{"/task", "kill"}, parts[1:]...), theme, w, kiReader)
		return true, false
	case "/get_task_status", "/task_status", "/taskstatus", "/task_view", "/taskview", "/status":
		handleTaskCommand(a, append([]string{"/task", "status"}, parts[1:]...), theme, w, kiReader)
		return true, false
	case "/ps":
		if len(parts) > 1 {
			handleTaskCommand(a, append([]string{"/task", "status"}, parts[1:]...), theme, w, kiReader)
		} else {
			handleTaskCommand(a, []string{"/task", "list"}, theme, w, kiReader)
		}
		return true, false
	case "/task_list", "/tasklist":
		handleTaskCommand(a, []string{"/task", "list"}, theme, w, kiReader)
		return true, false
	case "/task_clear", "/taskclear":
		handleTaskCommand(a, []string{"/task", "clear"}, theme, w, kiReader)
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
	case "/compress", "/compact":
		handleCompressCommand(a, parts, messages, currentSessionID, theme, w, calcHistoryTokens)
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
	case "/tools", "/tool":
		handleToolsCommand(a, parts, messages, theme, w, kiReader, calcHistoryTokens)
		return true, false
	case "/extensions", "/extension":
		handleExtensionsCommand(a, theme, w)
		return true, false
	default:
		if HandlePromptTemplate(a, cmdName, strings.TrimSpace(strings.TrimPrefix(trimmed, cmdName)), messages, w) {
			return true, false
		}
		args, err := domaintool.SplitCommandArgs(strings.TrimSpace(strings.TrimPrefix(trimmed, cmdName)))
		if err != nil {
			fmt.Fprintln(w, err)
			return true, false
		}
		handledExt, errExt := ui.RunExtension(a, cmdName, args, messages, w)
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
