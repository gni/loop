package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/domain/task"
	"loop/pkg/ui/render/markdown"
	"loop/pkg/ui/render/syntax"
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
	_, after, ok := strings.Cut(content, "`")
	if !ok {
		return "", "", false
	}
	rest := after
	before, _, ok := strings.Cut(rest, "`")
	if !ok {
		return "", "", false
	}
	cmdStr := before
	var output string
	if _, after, ok := strings.Cut(rest, "\n"); ok {
		output = after
	}
	return cmdStr, output, true
}

func printPromptDivider(w io.Writer, borderStyle style.Style) {
	termW := markdown.RenderWidth(w)
	prefix := "─── prompt "
	available := max(1, termW-1)
	if available <= style.CellWidth(prefix) {
		fmt.Fprintln(w, borderStyle.Render(style.TruncateCells(prefix, available)))
		return
	}
	dashesCount := max(available-style.CellWidth(prefix), 1)
	fmt.Fprintln(w, borderStyle.Render(prefix+strings.Repeat("─", dashesCount)))
}

func renderToolTitleLine(w io.Writer, toolName, argsJSON string, status ToolRenderStatus, theme style.UITheme, bashSyntaxColor bool) {
	symbol := RenderToolSymbol(toolName, status, theme)
	path := ExtractToolTarget(toolName, argsJSON)
	if toolName == "bash" || IsCommandLikeTool(toolName) {
		fmt.Fprintln(w, FormatBashCommandLineStyled(symbol, path, theme, bashSyntaxColor))
	} else {
		fmt.Fprintln(w, FormatToolTitleForWidth(symbol, toolName, path, theme, markdown.RenderWidth(w)))
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
		if cfg != nil && !cfg.MarkdownRender {
			hooks.RenderMarkdown = markdown.RenderRawMarkdownContent
		} else {
			hooks.RenderMarkdown = markdown.RenderMarkdownContent
		}
	}
	if hooks.RenderReasoning == nil {
		if cfg != nil && !cfg.MarkdownRender {
			hooks.RenderReasoning = markdown.RenderRawReasoningContent
		} else {
			hooks.RenderReasoning = markdown.RenderReasoningMarkdownContent
		}
	}
	if hooks.RenderError == nil {
		hooks.RenderError = func(w io.Writer, message string, theme style.UITheme) {
			RenderFailure(w, message, collapseResults, theme)
		}
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	textStyle := style.NewStyle().Foreground(theme.Text)
	spacing := agent.NewNewlineCounterWriter(w)
	w = spacing

	collapse := collapseResults || (cfg != nil && cfg.CollapseResults)
	bashSyntaxColor := cfg == nil || cfg.BashSyntaxColor

	var lastRole, lastUserPrompt string
	matches := message.MatchToolResults(messages)
	responses := make(map[[2]int]bool, len(matches))
	displayedResults := make(map[string]int)
	for _, match := range matches {
		responses[[2]int{match.AssistantIndex, match.CallIndex}] = true
	}
	for i, msg := range messages {
		if msg.Role != "tool" {
			clear(displayedResults)
		}
		if msg.IsContextSummary() {
			fmt.Fprintln(w, style.NewStyle().Foreground(theme.TextMuted).Italic(true).Render("[earlier conversation compressed; summary retained for the agent]"))
			fmt.Fprintln(w, agent.CompressionSummaryPreview(msg.Content))
			fmt.Fprintln(w)
			continue
		}
		if msg.IsInternalContext() {
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
				fmt.Fprintf(w, "%s%s\n", promptStyle.Render("▹ "), textStyle.Render(cmdStr))
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
				if bashSyntaxColor {
					cmdLines := syntax.HighlightBashCommandLines(strings.Split(strings.ReplaceAll(strings.ReplaceAll(cmdStr, "\r\n", "\n"), "\r", "\n"), "\n"), theme)
					for idx, styled := range cmdLines {
						if idx == 0 {
							fmt.Fprintf(w, "%s%s\n", promptStyle.Render("▹ !"), styled)
						} else {
							fmt.Fprintf(w, "   %s\n", styled)
						}
					}
				} else {
					fmt.Fprintf(w, "%s%s\n", promptStyle.Render("▹ !"), textStyle.Render(cmdStr))
				}
				if output != "" {
					if bashSyntaxColor {
						renderManualShellHistoryOutput(w, cmdStr, output, theme)
					} else {
						fmt.Fprint(w, output)
					}
				}
				lastRole = "user"
				continue
			}

			if strings.HasPrefix(msg.Content, "System Event: Background task ") || strings.HasPrefix(msg.Content, "Background task ") {
				if lastRole != "" {
					fmt.Fprintln(w)
				}
				fmt.Fprintf(w, "%s\n", style.NewStyle().Foreground(theme.TextMuted).Italic(true).Render("✦ "+msg.Content))
				lastRole = "system_event"
				continue
			}

			if lastRole != "" {
				spacing.EnsureActivityGap()
			}
			printPromptDivider(w, borderStyle)
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("▹ "), textStyle.Render(msg.Content))

		case "assistant":
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
				for range style.ActivityGapLines {
					fmt.Fprintln(w)
				}
			}
			fmt.Fprintln(w, style.ActivityDivider(theme))

			hasPrintedAnything := false
			if reasoningVisible {
				cleanReasoning := agent.StripEchoedPrompt(msg.ReasoningContent, lastUserPrompt)
				if strings.TrimSpace(cleanReasoning) != "" {
					effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
					if cfg.ShowThinking && effort != "off" && effort != "none" {
						if collapse {
							fmt.Fprintln(w, FormatThinkingSummary(theme, msg.ReasoningDuration, false, ThinkingLineCount(cleanReasoning), cfg.ShowMicroseconds))
						} else {
							hooks.RenderReasoning(w, strings.Trim(cleanReasoning, "\r\n"), theme)
							fmt.Fprintln(w)
							labelStyle := style.NewStyle().Foreground(theme.TextMuted).Italic(true)
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
			for callIndex, tc := range msg.ToolCalls {
				if tc.Function.Name == "task_state" {
					continue
				}
				if responses[[2]int{i, callIndex}] {
					continue
				}

				if printedUnrespondedToolCount > 0 {
					fmt.Fprintln(w)
				}
				renderToolTitleLine(w, tc.Function.Name, tc.Function.Arguments, ToolStatusPending, theme, bashSyntaxColor)

				if len(tc.Function.Arguments) > 0 && IsWriteLikeTool(tc.Function.Name) {
					RenderToolOutputWithOptions(w, "", false, collapse, theme, tc.Function.Name, tc.Function.Arguments, false, bashSyntaxColor)
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
			if match, ok := matches[i]; ok {
				toolName = match.Call.Function.Name
				argsJSON = match.Call.Function.Arguments
				displayedResults[msg.ToolCallID] = i
			} else if previous, exists := displayedResults[msg.ToolCallID]; exists {
				// Identical saved acknowledgements are one result. Preserve
				// differing output or execution evidence, even with the same ID.
				if equal, err := message.EqualPersisted(messages[previous], msg); err == nil && equal {
					continue
				}
			}
			if toolName == "" {
				toolName = msg.Name
			}

			lowerContent := strings.ToLower(msg.Content)
			isError := msg.IsError ||
				(toolName == "bash" && strings.HasPrefix(msg.Content, "[Command Failed: ")) ||
				(toolName != "read" && toolName != "grep" && strings.HasPrefix(lowerContent, "error:"))
			if toolName == "task_state" && !isError && !task.ShowProgressNotice(msg.Content, argsJSON) {
				continue
			}
			status := ToolStatusSuccess
			if isError {
				status = ToolStatusError
			}

			if lastRole == "tool" || (lastRole == "assistant" && i > 0 && strings.TrimSpace(messages[i-1].Content) != "") {
				for range style.ActivityGapLines {
					fmt.Fprintln(w)
				}
			}
			if toolName != "task_state" {
				renderToolTitleLine(w, toolName, argsJSON, status, theme, bashSyntaxColor)
			}
			RenderToolOutputWithOptions(w, msg.Content, isError, collapse, theme, toolName, argsJSON, false, bashSyntaxColor)

		case "error":
			if lastRole != "" {
				fmt.Fprintln(w)
			}
			hooks.RenderError(w, msg.Content, theme)
		}
		lastRole = msg.Role
	}
}

func renderManualShellHistoryOutput(w io.Writer, cmdStr, output string, theme style.UITheme) {
	if rest, ok := strings.CutPrefix(output, "stdout:\n"); ok {
		fmt.Fprintln(w, style.NewStyle().Foreground(theme.TextMuted).Render("stdout:"))
		sw := syntax.NewShellOutputWriter(w, cmdStr, theme)
		_, _ = io.WriteString(sw, rest)
		return
	}
	sw := syntax.NewShellOutputWriter(w, cmdStr, theme)
	_, _ = io.WriteString(sw, output)
}
