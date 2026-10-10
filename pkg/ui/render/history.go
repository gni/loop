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

	var lastRole string
	var lastUserPrompt string
	for i, msg := range messages {
		if msg.Role == "system" {
			continue
		}

		if msg.Role == "user" {
			lastUserPrompt = msg.Content
			if strings.HasPrefix(msg.Content, "[user manually executed slash command: `") {
				firstTick := strings.Index(msg.Content, "`")
				if firstTick != -1 {
					rest := msg.Content[firstTick+1:]
					secondTick := strings.Index(rest, "`")
					if secondTick != -1 {
						cmdStr := rest[:secondTick]
						output := ""
						newLineIdx := strings.Index(rest, "\n")
						if newLineIdx != -1 {
							output = rest[newLineIdx+1:]
						}
						if lastRole != "" && lastRole != "tool" {
							fmt.Fprintln(w)
						}
						termW, _ := terminal.GetDimensions()
						dashesCount := termW - 12
						if dashesCount < 5 {
							dashesCount = 5
						}
						separator := "─── prompt " + strings.Repeat("─", dashesCount)
						fmt.Fprintln(w, borderStyle.Render(separator))
						fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), cmdStr)
						if output != "" {
							fmt.Fprint(w, output)
						}
						lastRole = "user"
						continue
					}
				}
			}

			if strings.HasPrefix(msg.Content, "[user manually executed local shell command: `") {
				firstTick := strings.Index(msg.Content, "`")
				if firstTick != -1 {
					rest := msg.Content[firstTick+1:]
					secondTick := strings.Index(rest, "`")
					if secondTick != -1 {
						cmdStr := rest[:secondTick]
						output := ""
						newLineIdx := strings.Index(rest, "\n")
						if newLineIdx != -1 {
							output = rest[newLineIdx+1:]
						}
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
				}
			}

			if strings.HasPrefix(msg.Content, "System Event: Background task ") || strings.HasPrefix(msg.Content, "Background task ") {
				if lastRole != "" {
					fmt.Fprintln(w)
				}
				eventStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
				fmt.Fprintf(w, "%s\n", eventStyle.Render("✦ "+msg.Content))
				lastRole = "system_event"
				continue
			}

			if lastRole != "" {
				fmt.Fprintln(w)
			}
			termW, _ := terminal.GetDimensions()
			dashesCount := termW - 12
			if dashesCount < 5 {
				dashesCount = 5
			}
			separator := "─── prompt " + strings.Repeat("─", dashesCount)
			fmt.Fprintln(w, borderStyle.Render(separator))
			fmt.Fprintf(w, "%s%s\n", promptStyle.Render("> "), msg.Content)
		} else if msg.Role == "assistant" {
			divider := style.NewStyle().Foreground(theme.Border).Render(strings.Repeat("╌", 40))
			fmt.Fprintln(w, divider)

			hasPrintedAnything := false
			if msg.ReasoningContent != "" {
				cleanReasoning := agent.StripEchoedPrompt(msg.ReasoningContent, lastUserPrompt)
				if strings.TrimSpace(cleanReasoning) != "" {
					effort := strings.ToLower(strings.TrimSpace(cfg.ReasoningEffort))
					isThinkingEnabled := cfg.ShowThinking && effort != "off" && effort != "none"
					if isThinkingEnabled {
						if hooks.RenderReasoning != nil {
							hooks.RenderReasoning(w, strings.TrimRight(cleanReasoning, "\r\n"), theme)
						} else {
							fmt.Fprintln(w, style.NewStyle().Foreground(theme.TextMuted).Render(cleanReasoning))
						}
						fmt.Fprintln(w)

						labelStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
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
				if hooks.RenderMarkdown != nil {
					hooks.RenderMarkdown(w, assistantContent, theme)
				} else {
					fmt.Fprintln(w, assistantContent)
				}
				fmt.Fprintln(w)
				if len(msg.ToolCalls) > 0 {
					fmt.Fprintln(w)
				}
				hasPrintedAnything = true
			} else if hasPrintedAnything && len(msg.ToolCalls) > 0 {
				fmt.Fprintln(w)
			}

			printedUnrespondedToolCount := 0
			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					hasResponse := false
					for j := i + 1; j < len(messages); j++ {
						if messages[j].Role == "tool" && messages[j].ToolCallID == tc.ID {
							hasResponse = true
							break
						}
					}

					if !hasResponse {
						if printedUnrespondedToolCount > 0 {
							fmt.Fprintln(w)
						}

						isWriteTool := tc.Function.Name == "write" || strings.Contains(tc.Function.Name, "write") || strings.Contains(tc.Function.Name, "replace")
						path := ExtractToolTarget(tc.Function.Name, tc.Function.Arguments)
						symbol := RenderToolSymbol(tc.Function.Name, ToolStatusPending, theme)
						if tc.Function.Name == "bash" {
							fmt.Fprintln(w, FormatBashCommandLine(symbol, path, theme))
						} else {
							title := FormatToolTitle(symbol, tc.Function.Name, path, theme)
							fmt.Fprintln(w, title)
						}

						if len(tc.Function.Arguments) > 0 && isWriteTool {
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
							cancelStyle := style.NewStyle().Foreground(theme.Error).Italic(true)
							fmt.Fprintln(w, cancelStyle.Render("  [operation cancelled]"))
						}
						hasPrintedAnything = true
						printedUnrespondedToolCount++
					}
				}
			}
		} else if msg.Role == "tool" {
			var toolName string
			var argsJSON string
			for j := i - 1; j >= 0; j-- {
				m := messages[j]
				if m.Role == "assistant" {
					for _, tc := range m.ToolCalls {
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
			symbol := RenderToolSymbol(toolName, status, theme)
			path := ExtractToolTarget(toolName, argsJSON)
			if toolName == "bash" {
				fmt.Fprintln(w, FormatBashCommandLine(symbol, path, theme))
			} else {
				title := FormatToolTitle(symbol, toolName, path, theme)
				fmt.Fprintln(w, title)
			}
			collapse := false
			if cfg != nil {
				collapse = cfg.CollapseResults
			}
			RenderToolOutput(w, msg.Content, isError, collapse, theme, toolName, argsJSON, false)
		} else if msg.Role == "error" {
			if lastRole != "" {
				fmt.Fprintln(w)
			}
			if hooks.RenderError != nil {
				hooks.RenderError(w, msg.Content, theme)
			} else {
				fmt.Fprintln(w, style.NewStyle().Foreground(theme.Error).Bold(true).Render(msg.Content))
			}
		}
		lastRole = msg.Role
	}
}
