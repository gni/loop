package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/ui"
	"loop/pkg/ui/commands"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func renderTurnPromptHeader(
	w io.Writer,
	a *agent.Agent,
	theme style.UITheme,
	promptPrefix, line string,
	fromQueue bool,
	kiReader *interceptor.KeyInterceptorReader,
) {
	width, _ := ui.GetTerminalSize()
	live := a.LiveSettings()
	branch := ""
	if live.ShowGitBranch {
		branch = ui.CachedGitBranch()
	}
	effort := live.ReasoningEffort
	if !live.ShowReasoningBadge {
		effort = "hidden"
	}
	fmt.Fprint(w, render.FormatPromptSeparatorWithAuto(live.ShowThinking, effort, live.ShowAutoBadge, live.AutoApprove, theme, width, branch))

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	textStyle := style.NewStyle().Foreground(theme.Text)
	if fromQueue {
		remaining := 0
		if kiReader != nil {
			remaining = kiReader.QueueLen()
		}
		queueInfo := " [from queue]"
		if remaining > 0 {
			queueInfo = fmt.Sprintf(" [from queue - %d remaining]", remaining)
		}
		queueBadge := style.NewStyle().Foreground(theme.Highlight).Bold(true).Render(queueInfo)
		fmt.Fprintf(w, "%s%s%s\n", promptStyle.Render(promptPrefix), textStyle.Render(line), queueBadge)
	} else {
		fmt.Fprintf(w, "%s%s\n", promptStyle.Render(promptPrefix), textStyle.Render(line))
	}

	divider := style.ActivityDivider(theme)
	fmt.Fprintln(w, divider)
}

func executeREPLTurn(
	a *agent.Agent,
	mam *swarm.MultiAgentManager,
	ppWriter *ui.PromptPreservingWriter,
	messages *[]db.Message,
	line string,
	allowedTools []string,
	theme style.UITheme,
	currentSessionID string,
	fd int,
	kiReader *interceptor.KeyInterceptorReader,
	attachmentSource *string,
	preExpanded bool,
	fromQueue bool,
) {
	parent := kiReader.InputContext
	if parent == nil {
		parent = context.Background()
	}
	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatsText = ""
	ui.GetUI().StateMu.Unlock()
	if active := mam.CurrentActiveAgent(); active != nil {
		ui.GetUI().SetActiveCancel(active.CancelActiveTurn)

		restore, restoreErr := ui.SetNonCanonical(fd)
		if restoreErr == nil && restore != nil {
			defer restore()
		}

		wasCancelled := false
		if err := mam.SendMessage(active.Name, line); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
		} else {
			select {
			case <-parent.Done():
				active.CancelActiveTurn()
				wasCancelled = true
			case <-active.Context.Done():
				fmt.Fprintln(os.Stderr, "agent context cancelled.")
				wasCancelled = true
			case <-active.Output:
			}
			ppWriter.EnsureNewline()
		}

		ui.GetUI().SetActiveCancel(nil)
		if wasCancelled {
			kiReader.ResetTypeAhead()
			kiReader.ClearQueue()
			kiReader.NotifyQueueChanged()
			kiReader.Drain()
		}
		return
	}

	ctx, cancel := context.WithCancel(parent)
	if attachmentSource != nil {
		ctx = resources.WithAttachmentSource(ctx, line, *attachmentSource)
	}
	if preExpanded {
		ctx = resources.WithPreExpandedPrompt(ctx, line)
	}
	ui.GetUI().SetActiveCancel(cancel)
	if !fromQueue {
		kiReader.ResetTypeAhead()
	}

	// During nonstop execution, ordinary queued prompts steer the next model
	// turn. Slash commands, help aliases, and manual shell commands remain
	// local commands and are never sent to the model.
	previousInput := a.NextInput
	nostopEnabled := false
	if cfgSnapshot, cfgErr := a.Configuration(); cfgErr == nil && cfgSnapshot != nil {
		nostopEnabled = cfgSnapshot.Nostop.Enabled()
	}
	if nostopEnabled && kiReader != nil {
		a.NextInput = func() []db.Message {
			if previousInput != nil {
				if input := previousInput(); len(input) > 0 {
					return input
				}
			}
			directCommands := false
			if cfgSnapshot, cfgErr := a.Configuration(); cfgErr == nil && cfgSnapshot != nil {
				directCommands = cfgSnapshot.DirectCommands
			}
			next, ok := kiReader.DequeuePromptIf(func(candidate interceptor.PromptInput) bool {
				source, _ := interceptor.PastedInputSource(candidate.Text, candidate.Pastes)
				trimmedSource := strings.TrimSpace(source)
				if strings.HasPrefix(trimmedSource, "/") || commands.IsHelpCommand(trimmedSource) || commands.IsBuiltinSlashCommand(strings.TrimSpace(candidate.Text)) {
					return false
				}
				if isManual, _ := parseManualCommand(source, directCommands); isManual {
					return false
				}
				return true
			})
			if !ok {
				return nil
			}
			source, pasted := interceptor.PastedInputSource(next.Text, next.Pastes)
			expanded := interceptor.ExpandPromptCodeBlocks(next.Text, next.CodeBlocks)
			if kiReader.Hist != nil && !strings.HasPrefix(expanded, "/") {
				kiReader.Hist.Add(expanded)
			}
			attachSource := expanded
			if pasted {
				attachSource = source
			}
			msg := db.Message{Role: "user", Content: expanded}
			if attached, attachBlocks, attachErr := resources.AttachmentsContext(ctx, attachSource, a.GetWorkspaceRoot(), a.SafePath); attachErr == nil {
				msg.Content += strings.TrimPrefix(attached, attachSource)
				msg.ContentBlocks = attachBlocks
			}
			kiReader.NotifyQueueChanged()
			live := a.LiveSettings()
			liveTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
			renderTurnPromptHeader(ppWriter, a, liveTheme, ui.GetPromptSymbol(nil), expanded, true, kiReader)
			return []db.Message{msg}
		}
		defer func() { a.NextInput = previousInput }()
	}

	restore, err := ui.SetNonCanonical(fd)

	if a.RunSession != nil {
		if _, runErr := a.RunSession(ctx, ppWriter, messages, line, allowedTools, theme, currentSessionID); runErr != nil && ctx.Err() == nil {
			recorded := false
			if messages != nil && len(*messages) > 0 {
				last := (*messages)[len(*messages)-1]
				recorded = last.Role == "error" && last.Content == runErr.Error()
			}
			if !recorded {
				render.RenderFailure(ppWriter, runErr.Error(), a.LiveSettings().CollapseResults, theme)
			}
			width, _ := ui.GetTerminalSize()
			label := style.TruncateRunes("Could not send prompt: "+runErr.Error(), max(1, width-1))
			ui.DrawStaticStatsLine(os.Stderr, theme, "", style.NewStyle().Foreground(theme.Error).Render(label))
		}
	} else {
		a.RunAgentLoop(ctx, ppWriter, messages, line, allowedTools, theme, false, currentSessionID)
	}

	if err == nil && restore != nil {
		restore()
	}
	wasCancelled := ctx.Err() != nil
	cancel()
	ui.GetUI().StateMu.Lock()
	ui.GetUI().ActiveCancelFunc = nil
	if wasCancelled {
		// Ctrl+C can arrive as SIGINT in noncanonical mode rather than as
		// an input byte. Apply the same cleanup as the keyboard dispatcher.
		ui.GetUI().State.QueuedPromptsCount = 0
	}
	ui.GetUI().StateMu.Unlock()
	if wasCancelled {
		kiReader.ResetTypeAhead()
		cleared := kiReader.ClearQueue()
		kiReader.NotifyQueueChanged()
		if cleared > 0 {
			live := a.LiveSettings()
			liveTheme := style.ResolveConfiguredTheme(live.Theme, live.SyntaxTheme)
			qStyle := style.NewStyle().Foreground(liveTheme.TextMuted).Italic(true)
			fmt.Fprintf(ppWriter, "\n%s\n", qStyle.Render(fmt.Sprintf("[queue cleared: %d item(s)]", cleared)))
		}
		ui.DrawStaticStatsLine(os.Stderr, theme, "", "")
		kiReader.Drain()
	}
}
