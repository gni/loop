package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func formatTurnElapsed(startTime time.Time, theme style.UITheme) string {
	elapsed := time.Since(startTime)
	timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
	return style.NewStyle().Foreground(theme.Border).Render(timeStr)
}

func formatCompletionTokensBadge(tokens int, theme style.UITheme) string {
	currentCStr := fmt.Sprintf("%d out", tokens)
	if tokens >= 1000 {
		currentCStr = fmt.Sprintf("%.1fk out", float64(tokens)/1000.0)
	}
	cStyled := style.NewStyle().Foreground(theme.Highlight).Render(currentCStr)
	dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")
	return cStyled + dotStyled
}

func (a *Agent) renderTurnError(w io.Writer, ncw io.Writer, sink HistorySink, err error, ctxErr error, isNonInteractive bool, theme style.UITheme) {
	if ctxErr != nil {
		if !isNonInteractive {
			fmt.Fprintln(w)
			cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
			fmt.Fprintln(w, cancelStyle.Render("[operation cancelled]"))
		}
		return
	}

	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	if isNonInteractive {
		fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
		return
	}

	fmt.Fprintln(w)
	if a.UI != nil {
		a.UI.RenderGenerationError(w, err.Error(), theme)
	} else {
		fmt.Fprintf(w, "\n%s %v\n", errStyle.Render("Error during generation:"), err)
	}
	sink.Append(db.Message{Role: "error", Content: err.Error()})
}

func (a *Agent) RunAgentLoop(ctx context.Context, w io.Writer, messages *[]db.Message, prompt string, allowlist []string, theme style.UITheme, isNonInteractive bool, sessionID string) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return
	}

	if a.TurnStartTime.IsZero() {
		a.TurnStartTime = time.Now()
	}
	startTime := a.TurnStartTime
	defer func() {
		a.TurnStartTime = time.Time{}
	}()

	writerToUse := w
	rawW := UnwrapWriter(writerToUse)
	a.SetTurnState(writerToUse, ctx, theme)
	defer a.ClearTurnState()

	if a.Guided != nil {
		a.Guided.ResetForNewUserTurn(a.GetTodos())
	}

	var loader *turnLoader
	if a.UI != nil && !isNonInteractive {
		loader = a.newTurnLoader(ctx, rawW, theme, startTime)
		a.CurrentLoader = loader
		defer func() {
			a.CurrentLoader = nil
			loader.Stop()
		}()
	}

	timePrinted := false
	defer func() {
		if !timePrinted {
			fmt.Fprintln(writerToUse)
			fmt.Fprintln(writerToUse, formatTurnElapsed(startTime, theme))
		}
	}()

	a.SyncSessionSystemPrompt(messages, sessionID)

	*messages = append(*messages, db.Message{Role: "user", Content: prompt})
	if sessionID != "" {
		if !db.HasMessages(sessionID) && len(*messages) > 1 && (*messages)[0].Role == "system" {
			if err := db.SaveMessage(sessionID, (*messages)[0]); err != nil {
				a.DebugLogError(sessionID, "db.SaveMessage (system prompt)", err)
			}
		}
		if err := db.SaveMessage(sessionID, (*messages)[len(*messages)-1]); err != nil {
			a.DebugLogError(sessionID, "db.SaveMessage (user prompt)", err)
		}
	}

	a.DebugLogUserCommand(sessionID, prompt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}

	engine := NewTurnEngine(a, EnginePolicy{
		SessionKey:    sessionID,
		AllowApproval: true,
		RunHooks:      true,
		GuidedMode:    a.IsGuidedMode(),
		ShowRecapLine: !isNonInteractive,
	})
	sink := NewAgentSink(a, messages, sessionID)
	timer := GetTimingRecorder()

	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
		}

		engine.BeginTurn()

		globalPromptTokensEst, _ := a.GetGlobalTokens(*messages, allowlist)
		effectiveLimit := a.GetEffectiveContextLimit(globalPromptTokensEst)
		timer.Time("compaction", "start")
		globalPromptTokensEst, effectiveLimit, _ = a.maybeCompress(ctx, messages, sessionID, theme, writerToUse, allowlist, globalPromptTokensEst, 0, effectiveLimit)
		timer.Time("compaction", "end")

		if engine.ShouldRecap() {
			engine.MaybeRecap(sink, globalPromptTokensEst, a.GetSessionTotalCompletionTokens(*messages), effectiveLimit, writerToUse, theme)
			globalPromptTokensEst, _ = a.GetGlobalTokens(*messages, allowlist)
			effectiveLimit = a.GetEffectiveContextLimit(globalPromptTokensEst)
		}

		if iter > 1 {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(writerToUse, divider)
		}

		toolsForLog := a.Registry.GetAvailableTools(allowlist)
		a.DebugLogLLMRequest(sessionID, iter, a.Config.Model, a.Config.Endpoint, *messages, toolsForLog)

		chunkChan := make(chan StreamChunk, 200)
		streamErrChan := make(chan error, 1)
		var assistantMsg *db.Message

		timer.Time("llm", "start")
		go func() {
			msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, chunkChan)
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
			streamErrChan <- err
		}()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = new(bytes.Buffer)
		a.CurrentStreamMu.Unlock()

		teeWriter := &CustomTeeWriter{screen: writerToUse, buffer: a.CurrentStreamBuffer}
		ncw := &NewlineCounterWriter{Writer: teeWriter}
		a.SetCurrentWriter(ncw)
		sr := a.NewTurnStreamRenderer(ncw, theme, "loop", prompt)

		echoFilter := NewPromptEchoFilter(prompt)
		priorCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		updateStreamStatus := func(generating bool) {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, effectiveLimit, generating, 0, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		var stopOnce sync.Once
		stopTicker := func() {
			stopOnce.Do(func() {
				updateStreamStatus(false)
			})
		}
		defer stopTicker()

		updateStreamStatus(true)

		for chunk := range chunkChan {
			switch chunk.Type {
			case "reasoning":
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				if cleaned := echoFilter.Write(chunk.Content); cleaned != "" {
					sr.WriteReasoning(cleaned)
				}
			case "text":
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				sr.Write(chunk.Content)
			case "tool_name":
				sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
				if loader != nil {
					loader.ShowDots()
					loader.Feed()
				}
			case "tool_target":
				sr.SetToolTarget(chunk.Content, chunk.ToolCallIndex)
			case "tool_call":
				sr.WriteToolCall(chunk.Content)
				if loader != nil {
					if sr.DidStreamToolBody(chunk.ToolCallIndex) {
						loader.PauseDots()
					}
					loader.Feed()
				}
			}
		}
		timer.Time("llm", "end")

		if pending := echoFilter.Flush(); pending != "" {
			sr.WriteReasoning(pending)
		}
		if loader != nil {
			loader.ShowDots()
		}

		sr.Flush()
		stopTicker()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = nil
		a.CurrentStreamMu.Unlock()

		if streamErr := <-streamErrChan; streamErr != nil {
			if loader != nil {
				loader.Stop()
			}
			a.renderTurnError(writerToUse, ncw, sink, streamErr, ctx.Err(), isNonInteractive, theme)
			return
		}

		if assistantMsg == nil {
			return
		}

		if prompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, prompt)
		}

		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()
		a.DebugLogLLMResponse(sessionID, iter, assistantMsg, a.LastGenerationDuration())
		sink.Append(*assistantMsg)

		globalPromptTokens, _ := a.GetGlobalTokens(*messages, allowlist)
		globalCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)
		postEffectiveLimit := a.GetEffectiveContextLimit(globalPromptTokens)
		timer.Time("compaction", "start")
		globalPromptTokens, postEffectiveLimit, _ = a.maybeCompress(ctx, messages, sessionID, theme, writerToUse, allowlist, globalPromptTokens, assistantMsg.CompletionTokens, postEffectiveLimit)
		timer.Time("compaction", "end")

		var finalTps float64
		if dur := a.LastGenerationDuration(); dur > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / dur.Seconds()
		}

		syncPostTurnStatus := func() {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokens, globalCompletionTokens, assistantMsg.CompletionTokens, postEffectiveLimit, false, finalTps, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		if len(assistantMsg.ToolCalls) == 0 {
			if loader != nil {
				loader.Stop()
			}
			timePrinted = true
			timeStyled := formatTurnElapsed(startTime, theme)

			statsText := timeStyled
			if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
				statsText = formatCompletionTokensBadge(assistantMsg.CompletionTokens, theme) + timeStyled
			}

			_, height := style.GetTerminalSize()
			if height > 0 && a.UI != nil {
				a.UI.DrawStatsLine(rawW, theme, "", statsText)
			} else {
				fmt.Fprintln(writerToUse, statsText)
			}

			if dropped := a.TurnDroppedChunks(); dropped > 0 && a.UI != nil {
				a.UI.RenderGenerationError(writerToUse, fmt.Sprintf("output truncated: %d stream chunk(s) could not be delivered to the UI", dropped), theme)
			}

			syncPostTurnStatus()
			return
		}

		syncPostTurnStatus()

		timer.Time("tool", "start")
		halted, aborted := engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, loader, ncw, theme, NewMainRenderer(a), func(tc db.ToolCall, output string, toolErr error) {
			a.DebugLogToolExecution(sessionID, iter, tc.Function.Name, tc.Function.Arguments, output, toolErr, 0)
		})
		timer.Time("tool", "end")
		if halted || aborted {
			return
		}
	}

	if loader != nil {
		loader.Stop()
	}
	errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
	fmt.Fprintf(writerToUse, "\n%s reached maximum reasoning steps limit (%d).\n", errStyle.Render("warning:"), maxSteps)
}