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
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)
			fmt.Fprintln(writerToUse)
			fmt.Fprintln(writerToUse, timeStyled)
		}
	}()

	if len(*messages) > 0 && (*messages)[0].Role == "system" {
		currentSysPrompt := a.GetSystemPrompt()
		a.stateMu.Lock()
		forceUpdate := a.ForceSystemPromptUpdate
		a.ForceSystemPromptUpdate = false
		a.stateMu.Unlock()
		if (*messages)[0].Content != currentSysPrompt || forceUpdate {
			(*messages)[0].Content = currentSysPrompt
			if sessionID != "" {
				if err := db.RewriteSession(sessionID, *messages); err != nil {
					a.DebugLogError(sessionID, "db.RewriteSession (system prompt)", err)
				}
			}
		}
	}

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
		effort := strings.ToLower(strings.TrimSpace(a.Config.ReasoningEffort))
		enableThinking := a.Config.ShowThinking && effort != "off" && effort != "none"
		var sr StreamRenderer
		if a.UI != nil {
			sr = a.UI.NewStreamRenderer(ncw, theme, enableThinking, a.Config.StreamWrites, "loop")
		} else {
			sr = &FallbackStreamRenderer{w: ncw}
		}
		sr.SetPrompt(prompt)

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
			if ctx.Err() != nil {
				if !isNonInteractive {
					fmt.Fprintln(writerToUse)
					cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
					fmt.Fprintln(writerToUse, cancelStyle.Render("[operation cancelled]"))
				}
			} else if !isNonInteractive {
				fmt.Fprintln(writerToUse)
				if a.UI != nil {
					a.UI.RenderGenerationError(writerToUse, streamErr.Error(), theme)
				} else {
					errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
					fmt.Fprintf(writerToUse, "\n%s %v\n", errStyle.Render("Error during generation:"), streamErr)
				}
				sink.Append(db.Message{Role: "error", Content: streamErr.Error()})
			} else {
				errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
				fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), streamErr)
			}
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
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)

			statsText := timeStyled
			if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
				currentCStr := fmt.Sprintf("%d out", assistantMsg.CompletionTokens)
				if assistantMsg.CompletionTokens >= 1000 {
					currentCStr = fmt.Sprintf("%.1fk out", float64(assistantMsg.CompletionTokens)/1000.0)
				}
				cStyled := style.NewStyle().Foreground(theme.Highlight).Render(currentCStr)
				dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")
				statsText = cStyled + dotStyled + timeStyled
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
		halted, aborted := engine.ExecuteToolCalls(ctx, assistantMsg, sink, sr, loader, ncw, theme, NewMainRenderer(a), nil)
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