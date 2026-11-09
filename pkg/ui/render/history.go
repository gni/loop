package render

import (
	"fmt"
	"io"
	"loop/pkg/domain/task"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

type HistoryRenderHooks struct {
	RenderMarkdown  func(w io.Writer, content string, theme style.UITheme)
	RenderReasoning func(w io.Writer, content string, theme style.UITheme)
	RenderError     func(w io.Writer, content string, theme style.UITheme)
}

func extractBacktickCommandAndOutput(content, prefix string) (string, string, bool) {
	if !strings.HasPrefix(content, prefix) {
		return "", "", false
	}
	firstTick := strings.Index(content, "`")
	if firstTick == -1 {
		return "", "", false
	}
	rest := content[firstTick+1:]
	secondTick := strings.Index(rest, "`")
	if secondTick == -1 {
		return "", "", false
	}
	cmdStr := rest[:secondTick]
	var output string
	if nl := strings.Index(rest, "\n"); nl != -1 {
		output = rest[nl+1:]
	}
	return cmdStr, output, true
}

func printPromptDivider(w io.Writer, borderStyle style.Style) {
	termW, _ := terminal.GetDimensions()
	dashesCount := termW - 12
	if dashesCount < 5 {
		dashesCount = 5
	}
	fmt.Fprintln(w, borderStyle.Render("─── prompt "+strings.Repeat("─", dashesCount)))
}

func renderToolTitleLine(w io.Writer, toolName, argsJSON string, status ToolRenderStatus, theme style.UITheme) {
	symbol := RenderToolSymbol(toolName, status, theme)
	path := ExtractToolTarget(toolName, argsJSON)
	if toolName == "bash" || IsCommandLikeTool(toolName) {
		fmt.Fprintln(w, FormatBashCommandLine(symbol, path, theme))
	} else {
		fmt.Fprintln(w, FormatToolTitle(symbol, toolName, path, theme))
	}
}

func PrintSessionHistory(
	w io.Writer,
	messages []db.Message,
	theme style.UITheme,
	cfg *config.Config,
	isGenerating bool,
	collapseResults bool,
	hooks HistoryRenderHooks,
) {
	if hooks.RenderMarkdown == nil {
		hooks.RenderMarkdown = RenderMarkdownContent
	}
	if hooks.RenderReasoning == nil {
		hooks.RenderReasoning = RenderReasoningMarkdownContent
	}
	if hooks.RenderError == nil {
		hooks.RenderError = func(w io.Writer, message string, theme style.UITheme) {
			RenderFailure(w, message, collapseResults, theme)
		}
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	spacing := agent.NewNewlineCounterWriter(w)
	w = spacing

	collapse := collapseResults || (cfg != nil && cfg.CollapseResults)

	var lastRole, lastUserPrompt string
	responses := make(map[string]int)
	for i, msg := range messages {
		if msg.Role == "tool" {
			responses[msg.ToolCallID] = i
		}
	}
	calls := make(map[string]db.ToolCall)
	for i, msg := range messages {
		if msg.Role == "system" {
			if strings.HasPrefix(msg.Content, "[System: Below is a summary of the earlier conversation history:") {
				fmt.Fprintln(w, borderStyle.Italic(true).Render("[earlier conversation compressed; summary retained for the agent]"))
				fmt.Fprintln(w, agent.CompressionSummaryPreview(msg.Content))
				fmt.Fprintln(w)
			}
			continue
		}

		switch msg.Role {
		case "user":
			lastUserPrompt = msg.Content
			if cmdStr, output, ok := extractBacktickCommandAndOutput(msg.Content, "[user manually executed slash command: `"); ok {
				if lastRole != "" && lastRole != "tool" {
					fmt.Fprintln(w)
				}
				printPromptDivider(w, borderStyle)
				fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), cmdStr)
				if output != "" {
					fmt.Fprint(w, output)
				}
				lastRole = "user"
				continue
			}

			if cmdStr, output, ok := extractBacktickCommandAndOutput(msg.Content, "[user manually executed local shell command: `"); ok {
				if lastRole != "" {
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> !"), cmdStr)
				if output != "" {
					fmt.Fprint(w, output)
				}
				lastRole = "user"
				continue
			}

			if strings.HasPrefix(msg.Content, "System Event: Background task ") || strings.HasPrefix(msg.Content, "Background task ") {
				if lastRole != "" {
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "%s\n", style.NewStyle().Foreground(theme.Border).Italic(true).Render("✦ "+msg.Content))
				lastRole = "system_event"
				continue
			}

			if lastRole != "" {
				spacing.EnsureActivityGap()
			}
			printPromptDivider(w, borderStyle)
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)

		case "assistant":
			for _, tc := range msg.ToolCalls {
				calls[tc.ID] = tc
			}
			reasoningVisible := cfg != nil && cfg.ShowThinking && strings.TrimSpace(msg.ReasoningContent) != ""
			if cfg != nil {
				effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
				reasoningVisible = reasoningVisible && effort != "off" && effort != "none"
			}
			onlyProgress := len(msg.ToolCalls) > 0 && strings.TrimSpace(agent.StripFallbackToolMarkup(msg.Content)) == "" && !reasoningVisible
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name != "task_state" {
					onlyProgress = false
				}
			}
			if onlyProgress {
				continue
			}
			if lastRole == "tool" {
				for line := 0; line < style.ActivityGapLines; line++ {
					fmt.Fprintln(w)
				}
			}
			fmt.Fprintln(w, style.ActivityDivider(theme))

			hasPrintedAnything := false
			if msg.ReasoningContent != "" {
				cleanReasoning := agent.StripEchoedPrompt(msg.ReasoningContent, lastUserPrompt)
				if strings.TrimSpace(cleanReasoning) != "" {
					effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
					if cfg.ShowThinking && effort != "off" && effort != "none" {
						if collapse {
							fmt.Fprintln(w, FormatThinkingSummary(theme, msg.ReasoningDuration, false, ThinkingLineCount(cleanReasoning), cfg.ShowMicroseconds))
						} else {
							hooks.RenderReasoning(w, strings.TrimRight(cleanReasoning, "\r\n"), theme)
							fmt.Fprintln(w)
							labelStyle := borderStyle.Italic(true)
							if msg.ReasoningDuration > 0 {
								fmt.Fprintf(w, "%s\n", labelStyle.Render("thought ("+style.FormatElapsedDuration(time.Duration(msg.ReasoningDuration*float64(time.Second)), cfg.ShowMicroseconds)+")"))
							} else {
								fmt.Fprintf(w, "%s\n", labelStyle.Render("thought"))
							}
						}
						hasPrintedAnything = true
					}
				}
			}

			assistantContent := agent.StripFallbackToolMarkup(msg.Content)
			if strings.TrimSpace(assistantContent) != "" {
				if hasPrintedAnything {
					fmt.Fprintln(w)
				}
				hooks.RenderMarkdown(w, strings.Trim(assistantContent, "\r\n"), theme)
				fmt.Fprintln(w)
				hasPrintedAnything = true
			} else if hasPrintedAnything && len(msg.ToolCalls) > 0 {
				fmt.Fprintln(w)
			}

			if hasPrintedAnything && strings.TrimSpace(assistantContent) != "" && len(msg.ToolCalls) > 0 && (i+1 >= len(messages) || messages[i+1].Role != "tool") {
				fmt.Fprintln(w)
			}
			printedUnrespondedToolCount := 0
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name == "task_state" {
					continue
				}
				if responses[tc.ID] > i {
					continue
				}

				if printedUnrespondedToolCount > 0 {
					fmt.Fprintln(w)
				}
				renderToolTitleLine(w, tc.Function.Name, tc.Function.Arguments, ToolStatusPending, theme)

				if len(tc.Function.Arguments) > 0 && IsWriteLikeTool(tc.Function.Name) {
					RenderToolOutput(w, "", false, collapse, theme, tc.Function.Name, tc.Function.Arguments, false)
				}

				if i == len(messages)-1 && isGenerating {
					runningStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)
					if collapse {
						fmt.Fprintln(w, runningStyle.Render("  [running... (collapsed)]"))
					} else {
						fmt.Fprintln(w, runningStyle.Render("  [running...]"))
					}
				} else {
					fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Italic(true).Render("  [operation cancelled]"))
				}
				printedUnrespondedToolCount++
			}

		case "tool":
			var toolName, argsJSON string
			if tc, ok := calls[msg.ToolCallID]; ok {
				toolName = tc.Function.Name
				argsJSON = tc.Function.Arguments
			}
			if toolName == "" {
				toolName = msg.Name
			}

			lowerContent := strings.ToLower(msg.Content)
			isError := msg.IsError || strings.HasPrefix(lowerContent, "error:") || strings.Contains(lowerContent, "command failed")
			if toolName == "task_state" && !isError && !task.ShowProgressNotice(msg.Content, argsJSON) {
				continue
			}
			status := ToolStatusSuccess
			if isError {
				status = ToolStatusError
			}

			if lastRole == "tool" || (lastRole == "assistant" && i > 0 && strings.TrimSpace(messages[i-1].Content) != "") {
				for line := 0; line < style.ActivityGapLines; line++ {
					fmt.Fprintln(w)
				}
			}
			if toolName != "task_state" {
				renderToolTitleLine(w, toolName, argsJSON, status, theme)
			}
			RenderToolOutput(w, msg.Content, isError, collapse, theme, toolName, argsJSON, false)

		case "error":
			if lastRole != "" {
				fmt.Fprintln(w)
			}
			hooks.RenderError(w, msg.Content, theme)
		}
		lastRole = msg.Role
	}
}
