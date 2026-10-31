package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

func renderTurnPromptHeader(
	w io.Writer,
	cfg *config.Config,
	theme style.UITheme,
	promptPrefix, line string,
	fromQueue bool,
	kiReader *interceptor.KeyInterceptorReader,
) {
	width, _ := ui.GetTerminalSize()
	fmt.Fprint(w, render.FormatPromptSeparator(cfg.ShowThinking, cfg.ReasoningEffort, theme, width))

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
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
		fmt.Fprintf(w, "%s%s%s\n", promptStyle.Render(promptPrefix), line, queueBadge)
	} else {
		fmt.Fprintf(w, "%s%s\n", promptStyle.Render(promptPrefix), line)
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
	fromQueue bool,
) {
	ui.GetUI().StateMu.Lock()
	ui.GetUI().LastStatsText = ""
	ui.GetUI().StateMu.Unlock()
	if active := mam.CurrentActiveAgent(); active != nil {
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = active.CancelActiveTurn
		ui.GetUI().StateMu.Unlock()

		if err := mam.SendMessage(active.Name, line); err != nil {
			fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
		} else {
			select {
			case <-active.Context.Done():
				fmt.Fprintln(os.Stderr, "agent context cancelled.")
			case <-active.Output:
			}
			ppWriter.EnsureNewline()
		}

		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
		kiReader.Drain()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	if attachmentSource != nil {
		ctx = resources.WithAttachmentSource(ctx, line, *attachmentSource)
	}
	ui.GetUI().StateMu.Lock()
	ui.GetUI().ActiveCancelFunc = cancel
	if !fromQueue {
		kiReader.ResetTypeAheadLocked()
	}
	ui.GetUI().StateMu.Unlock()

	// During nonstop execution, ordinary queued prompts steer the next model
	// turn. Slash commands remain commands and are never sent to the model.
	previousInput := a.NextInput
	if a.Config.Nostop.Enabled() && kiReader != nil {
		a.NextInput = func() []db.Message {
			if previousInput != nil {
				if input := previousInput(); len(input) > 0 {
					return input
				}
			}
			queued := kiReader.GetQueuedPrompts()
			if len(queued) == 0 {
				return nil
			}
			source, _ := interceptor.PastedInputSource(queued[0].Text, queued[0].Pastes)
			if strings.HasPrefix(strings.TrimSpace(source), "/") {
				return nil
			}
			next, ok := kiReader.DequeuePrompt()
			if !ok {
				return nil
			}
			ui.GetUI().StateMu.Lock()
			ui.GetUI().State.QueuedPromptsCount = kiReader.QueueLen()
			ui.GetUI().StateMu.Unlock()
			renderTurnPromptHeader(ppWriter, a.Config, theme, ui.GetPromptSymbol(a.Config), next.Text, true, kiReader)
			return []db.Message{{Role: "user", Content: next.Text}}
		}
		defer func() { a.NextInput = previousInput }()
	}

	restore, err := ui.SetNonCanonical(fd)

	if a.RunSession != nil {
		if _, runErr := a.RunSession(ctx, ppWriter, messages, line, allowedTools, theme, currentSessionID); runErr != nil && ctx.Err() == nil {
			render.RenderGenerationError(ppWriter, runErr.Error(), theme)
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
		kiReader.ResetTypeAheadLocked()
		ui.GetUI().State.QueuedPromptsCount = 0
	}
	ui.GetUI().StateMu.Unlock()
	if wasCancelled {
		kiReader.ClearQueue()
		cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
		ui.DrawStaticStatsLine(os.Stderr, theme, "", cancelStyle.Render("[operation cancelled]"))
	}
	kiReader.Drain()
}
