package render

import (
	"fmt"
	"io"
	"strings"

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
		hooks.RenderError = RenderGenerationError
	}

	borderStyle := style.NewStyle().Foreground(theme.Border)
	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)

	var lastRole, lastUserPrompt string
	for i, msg := range messages {
		if msg.Role == "system" {
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
				fmt.Fprintln(w)
			}
			printPromptDivider(w, borderStyle)
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)

		case "assistant":
			fmt.Fprintln(w, borderStyle.Render(strings.Repeat("╌", 40)))

			hasPrintedAnything := false
			if msg.ReasoningContent != "" {
				cleanReasoning := agent.StripEchoedPrompt(msg.ReasoningContent, lastUserPrompt)
				if strings.TrimSpace(cleanReasoning) != "" {
					effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
					if cfg.ShowThinking && effort != "off" && effort != "none" {
						hooks.RenderReasoning(w, strings.TrimRight(cleanReasoning, "\r\n"), theme)
						fmt.Fprintln(w)

						labelStyle := borderStyle.Italic(true)
						if msg.ReasoningDuration > 0 {
							fmt.Fprintf(w, "%s\n", labelStyle.Render(fmt.Sprintf("thought (%.1fs)", msg.ReasoningDuration)))
						} else {
							fmt.Fprintf(w, "%s\n", labelStyle.Render("thought"))
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
				hooks.RenderMarkdown(w, assistantContent, theme)
				fmt.Fprintln(w)
				if len(msg.ToolCalls) > 0 {
					fmt.Fprintln(w)
				}
				hasPrintedAnything = true
			} else if hasPrintedAnything && len(msg.ToolCalls) > 0 {
				fmt.Fprintln(w)
			}

			printedUnrespondedToolCount := 0
			for _, tc := range msg.ToolCalls {
				hasResponse := false
				for j := i + 1; j < len(messages); j++ {
					if messages[j].Role == "tool" && messages[j].ToolCallID == tc.ID {
						hasResponse = true
						break
					}
				}
				if hasResponse {
					continue
				}

				if printedUnrespondedToolCount > 0 {
					fmt.Fprintln(w)
				}
				renderToolTitleLine(w, tc.Function.Name, tc.Function.Arguments, ToolStatusPending, theme)

				if len(tc.Function.Arguments) > 0 && IsWriteLikeTool(tc.Function.Name) {
					RenderToolOutput(w, "", false, false, theme, tc.Function.Name, tc.Function.Arguments, false)
				}

				if i == len(messages)-1 && isGenerating {
					runningStyle := style.NewStyle().Foreground(theme.Secondary).Italic(true)
					if collapseResults {
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
			for j := i - 1; j >= 0; j-- {
				if messages[j].Role == "assistant" {
					for _, tc := range messages[j].ToolCalls {
						if tc.ID == msg.ToolCallID {
							toolName = tc.Function.Name
							argsJSON = tc.Function.Arguments
							break
						}
					}
				}
				if toolName != "" {
					break
				}
			}
			if toolName == "" {
				toolName = msg.Name
			}

			lowerContent := strings.ToLower(msg.Content)
			isError := strings.HasPrefix(lowerContent, "error:") || strings.Contains(lowerContent, "command failed")
			status := ToolStatusSuccess
			if isError {
				status = ToolStatusError
			}

			renderToolTitleLine(w, toolName, argsJSON, status, theme)
			RenderToolOutput(w, msg.Content, isError, cfg != nil && cfg.CollapseResults, theme, toolName, argsJSON, false)

		case "error":
			if lastRole != "" {
				fmt.Fprintln(w)
			}
			hooks.RenderError(w, msg.Content, theme)
		}
		lastRole = msg.Role
	}
}