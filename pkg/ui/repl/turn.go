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
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
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
	borderStyle := style.NewStyle().Foreground(theme.Border)
	statusStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	thinkingText := "off"
	effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
	if effort != "off" && effort != "none" && effort != "" {
		thinkingText = effort
	}
	statusPart := fmt.Sprintf("  [reasoning:%s]", thinkingText)
	prefix := "─── prompt "
	width, _ := ui.GetTerminalSize()
	statusLen := len(ui.StripAnsi(statusPart))
	prefixLen := len(prefix)
	dashesCount := width - prefixLen - statusLen - 2
	if dashesCount < 3 {
		dashesCount = 3
	}
	dashes := strings.Repeat("─", dashesCount)
	fmt.Fprintf(w, "%s%s\n", borderStyle.Render(prefix+dashes), statusStyle.Render(statusPart))

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

	divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
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
) {
	if mam.ActiveAgent != nil {
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = mam.ActiveAgent.CancelActiveTurn
		ui.GetUI().StateMu.Unlock()

		err := mam.SendMessage(mam.ActiveAgent.Name, line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error sending message: %v\n", err)
		} else {
			select {
			case <-mam.ActiveAgent.Context.Done():
				fmt.Fprintln(os.Stderr, "agent context cancelled.")
			case <-mam.ActiveAgent.Output:
			}
			fmt.Fprint(ppWriter, "\n\n")
		}

		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
		kiReader.Drain()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ui.GetUI().StateMu.Lock()
	ui.GetUI().ActiveCancelFunc = cancel
	kiReader.ResetTypeAheadLocked()
	ui.GetUI().StateMu.Unlock()

	restore, err := ui.SetNonCanonical(fd)

	a.RunAgentLoop(ctx, ppWriter, messages, line, allowedTools, theme, false, currentSessionID)

	if err == nil && restore != nil {
		restore()
	}
	cancel()
	ui.GetUI().StateMu.Lock()
	ui.GetUI().ActiveCancelFunc = nil
	ui.GetUI().StateMu.Unlock()
	kiReader.Drain()
}
