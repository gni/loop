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
	rawW := unwrapWriter(writerToUse)
	a.CurrentWriter = writerToUse
	a.CurrentContext = ctx
	a.CurrentTheme = theme
	defer func() {
		a.CurrentWriter = nil
		a.CurrentContext = nil
		a.CurrentTheme = style.UITheme{}
	}()

	var loader *turnLoader
	if a.UI != nil && !isNonInteractive {
		loader = a.newTurnLoader(ctx, rawW, theme, startTime)
		a.CurrentLoader = loader
		defer func() {
			a.CurrentLoader = nil
			loader.Stop()
		}()
	}

	var totalCompletionTokens int
	var totalPromptTokens int
	var totalApiDuration time.Duration

	timePrinted := false
	defer func() {
		if !timePrinted && prompt != "" {
			elapsed := time.Since(startTime)
			timeStr := fmt.Sprintf("%s (%.1fs)", time.Now().Format("2006-01-02 15:04:05"), elapsed.Seconds())
			timeStyled := style.NewStyle().Foreground(theme.Border).Render(timeStr)
			fmt.Fprintln(writerToUse)
			fmt.Fprintln(writerToUse, timeStyled)
		}
	}()

	if len(*messages) > 0 && (*messages)[0].Role == "system" {
		if (*messages)[0].Content == "" || a.ForceSystemPromptUpdate {
			currentSysPrompt := a.GetSystemPrompt()
			(*messages)[0].Content = currentSysPrompt
			a.ForceSystemPromptUpdate = false
			if sessionID != "" {
				_ = db.RewriteSession(sessionID, *messages)
			}
		}
	}

	*messages = append(*messages, db.Message{Role: "user", Content: prompt})
	if sessionID != "" {
		if !db.HasMessages(sessionID) {
			if len(*messages) > 1 && (*messages)[0].Role == "system" {
				_ = db.SaveMessage(sessionID, (*messages)[0])
			}
		}
		_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
	}

	a.DebugLogUserCommand(sessionID, prompt)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	maxSteps := a.Config.MaxReasoningSteps
	if maxSteps <= 0 {
		maxSteps = 30
	}
	guard := NewTurnExecutionGuard()
	if a.Config != nil {
		guard = NewTurnExecutionGuardWithConfig(a.Config.RepeatGuardLimit, a.Config.RepeatReminderThresholds)
	}
	consecutiveGuardRejections := 0
	for iter := 1; iter <= maxSteps; iter++ {
		if ctx.Err() != nil {
			return
		}

		if iter > 1 {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(writerToUse, divider)
		}

		autoAdapt := true
		if a.Config != nil {
			autoAdapt = a.Config.AutoAdaptContext
		}
		if autoAdapt {
			*messages = CompactHistoricalToolOutputs(*messages)
		}

		globalPromptTokensEst, _ := a.GetGlobalTokens(*messages, allowlist)
		effectiveLimit := a.GetEffectiveContextLimit(globalPromptTokensEst)

		thresh := 0.80
		if a.Config != nil && a.Config.CompressionThreshold > 0 {
			thresh = a.Config.CompressionThreshold
		}
		if len(*messages) > 4 && globalPromptTokensEst >= int(thresh*float64(effectiveLimit)) {
			a.CompressHistory(ctx, messages, sessionID, theme, writerToUse)
			globalPromptTokensEst, _ = a.GetGlobalTokens(*messages, allowlist)
			effectiveLimit = a.GetEffectiveContextLimit(globalPromptTokensEst)
		}

		toolsForLog := a.Registry.GetAvailableTools(allowlist)
		a.DebugLogLLMRequest(sessionID, iter, a.Config.Model, a.Config.Endpoint, *messages, toolsForLog)

		chunkChan := make(chan StreamChunk, 200)
		streamErrChan := make(chan error, 1)
		var assistantMsg *db.Message

		go func() {
			msg, err := a.StreamChatCompletions(ctx, *messages, allowlist, chunkChan)
			streamErrChan <- err
			if msg != nil {
				assistantMsg = msg
			}
			close(chunkChan)
		}()

		a.CurrentStreamMu.Lock()
		a.CurrentStreamBuffer = new(bytes.Buffer)
		a.CurrentStreamMu.Unlock()

		teeWriter := &CustomTeeWriter{screen: writerToUse, buffer: a.CurrentStreamBuffer}
		ncw := &NewlineCounterWriter{Writer: teeWriter}
		a.CurrentWriter = ncw
		effort := strings.ToLower(strings.TrimSpace(a.Config.ReasoningEffort))
		enableThinking := a.Config.ShowThinking && effort != "off" && effort != "none"
		var sr StreamRenderer
		if a.UI != nil {
			sr = a.UI.NewStreamRenderer(ncw, theme, enableThinking, a.Config.StreamWrites, "loop")
		} else {
			sr = &FallbackStreamRenderer{w: ncw}
		}
		sr.SetPrompt(prompt)

		// Suppress an echoed prompt during streaming, not just after the turn: chunks
		// are printed the moment they arrive, so post-hoc stripping cannot fix what the
		// user already saw.
		echoFilter := NewPromptEchoFilter(prompt)

		priorCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)

		updateStreamStatus := func(generating bool) {
			if a.UI != nil && !isNonInteractive {
				a.UI.UpdateStatus(a.Config.Model, globalPromptTokensEst, priorCompletionTokens, 0, effectiveLimit, generating, 0, a.CountActiveTasks(), a.Config.ShowTokens)
				a.UI.DrawStatusBar(rawW, theme)
			}
		}

		tickerDone := make(chan struct{})
		var tickerOnce sync.Once
		stopTicker := func() {
			tickerOnce.Do(func() {
				close(tickerDone)
				updateStreamStatus(false)
			})
		}
		defer stopTicker()

		updateStreamStatus(true)

		for chunk := range chunkChan {
			if chunk.Type == "reasoning" {
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				if cleaned := echoFilter.Write(chunk.Content); cleaned != "" {
					sr.WriteReasoning(cleaned)
				}
				continue
			} else if chunk.Type == "text" {
				if loader != nil {
					loader.PauseDots()
					loader.Feed()
				}
				sr.Write(chunk.Content)
			} else if chunk.Type == "tool_name" {
				sr.StartToolCall(chunk.Content, chunk.ToolCallIndex)
				if loader != nil {
					loader.ShowDots()
					loader.Feed()
				}
			} else if chunk.Type == "tool_call" {
				sr.WriteToolCall(chunk.Content)
				if sr.DidStreamToolBody(chunk.ToolCallIndex) {
					if loader != nil {
						loader.PauseDots()
						loader.Feed()
					}
				} else if loader != nil {
					loader.Feed()
				}
			}
		}
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

		streamErr := <-streamErrChan

		if streamErr != nil {
			if loader != nil {
				loader.Stop()
			}
			if ctx.Err() != nil {
				if !isNonInteractive {
					fmt.Fprintln(writerToUse)
					cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
					fmt.Fprintln(writerToUse, cancelStyle.Render("[operation cancelled]"))
				}
			} else {
				if !isNonInteractive {
					fmt.Fprintln(writerToUse)
					if a.UI != nil {
						a.UI.RenderGenerationError(writerToUse, streamErr.Error(), theme)
					} else {
						errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
						fmt.Fprintf(writerToUse, "\n%s %v\n", errStyle.Render("Error during generation:"), streamErr)
					}
					errMsg := db.Message{
						Role:    "error",
						Content: streamErr.Error(),
					}
					*messages = append(*messages, errMsg)
					if sessionID != "" {
						_ = db.SaveMessage(sessionID, errMsg)
					}
				} else {
					errStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
					fmt.Fprintf(ncw, "\n%s %v\n", errStyle.Render("Error during generation:"), streamErr)
				}
			}
			return
		}

		if assistantMsg == nil {
			return
		}

		if prompt != "" && assistantMsg.ReasoningContent != "" {
			assistantMsg.ReasoningContent = StripEchoedPrompt(assistantMsg.ReasoningContent, prompt)
		}

		totalCompletionTokens += assistantMsg.CompletionTokens
		totalPromptTokens += assistantMsg.PromptTokens
		totalApiDuration += a.LastGenerationDuration

		assistantMsg.ReasoningDuration = sr.GetReasoningDuration()
		a.DebugLogLLMResponse(sessionID, iter, assistantMsg, a.LastGenerationDuration)
		*messages = append(*messages, *assistantMsg)
		if sessionID != "" {
			_ = db.SaveMessage(sessionID, (*messages)[len(*messages)-1])
		}

		globalPromptTokens, _ := a.GetGlobalTokens(*messages, allowlist)
		globalCompletionTokens := a.GetSessionTotalCompletionTokens(*messages)
		postEffectiveLimit := a.GetEffectiveContextLimit(globalPromptTokens)
		if len(*messages) > 4 {
			thresh := 0.80
			if a.Config != nil && a.Config.CompressionThreshold > 0 {
				thresh = a.Config.CompressionThreshold
			}
			if totalTokens := globalPromptTokens + assistantMsg.CompletionTokens; totalTokens >= int(thresh*float64(postEffectiveLimit)) {
				a.CompressHistory(ctx, messages, sessionID, theme, writerToUse)
				globalPromptTokens, _ = a.GetGlobalTokens(*messages, allowlist)
				postEffectiveLimit = a.GetEffectiveContextLimit(globalPromptTokens)
			}
		}

		var finalTps float64
		if a.LastGenerationDuration > 0 {
			finalTps = float64(assistantMsg.CompletionTokens) / a.LastGenerationDuration.Seconds()
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

			currentCStr := fmt.Sprintf("%d out", assistantMsg.CompletionTokens)
			if assistantMsg.CompletionTokens >= 1000 {
				currentCStr = fmt.Sprintf("%.1fk out", float64(assistantMsg.CompletionTokens)/1000.0)
			}

			cStyled := style.NewStyle().Foreground(theme.Highlight).Render(currentCStr)
			dotStyled := style.NewStyle().Foreground(theme.Border).Render(" • ")

			var statsText string
			if a.Config.ShowTokens && assistantMsg.CompletionTokens > 0 {
				statsText = fmt.Sprintf("%s%s%s", cStyled, dotStyled, timeStyled)
			} else {
				statsText = timeStyled
			}

			_, height := style.GetTerminalSize()
			if height > 0 && a.UI != nil {
				a.UI.DrawStatsLine(rawW, theme, "", statsText)
			} else {
				fmt.Fprintln(writerToUse, statsText)
			}

			syncPostTurnStatus()
			return
		}

		syncPostTurnStatus()

		halted, aborted := a.executeToolCalls(
			ctx,
			assistantMsg,
			messages,
			sr,
			loader,
			ncw,
			theme,
			guard,
			sessionID,
			iter,
			&consecutiveGuardRejections,
		)
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

