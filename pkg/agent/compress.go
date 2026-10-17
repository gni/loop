package agent

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

func (a *Agent) CompressionThreshold() float64 {
	if a != nil && a.Config != nil && a.Config.CompressionThreshold > 0 {
		return a.Config.CompressionThreshold
	}
	return 0.95
}

func CompressionThresholdRatio() float64 {
	return 0.80
}

func CompactionTrigger(promptTokens, completionTokens, effectiveLimit int, ratio ...float64) bool {
	if effectiveLimit <= 0 || promptTokens <= 0 {
		return false
	}
	decision := promptTokens + completionTokens
	reserve := limits.CompactionReserveTokens()

	r := CompressionThresholdRatio()
	if len(ratio) > 0 && ratio[0] > 0 {
		r = ratio[0]
		ratioHeadroom := int((1.0 - r) * float64(effectiveLimit))
		const minSafeHeadroom = 4096
		if ratioHeadroom < reserve && ratioHeadroom >= minSafeHeadroom {
			reserve = ratioHeadroom
		}
	}

	return decision >= effectiveLimit-reserve || decision >= int(r*float64(effectiveLimit))
}

func (a *Agent) CompactionTrigger(promptTokens, completionTokens, effectiveLimit int) bool {
	thresh := CompressionThresholdRatio()
	if a != nil {
		thresh = a.CompressionThreshold()
	}
	return CompactionTrigger(promptTokens, completionTokens, effectiveLimit, thresh)
}

func (a *Agent) maybeCompress(ctx context.Context, messages *[]db.Message, sessionID string, theme style.UITheme, w io.Writer, allowlist []string, promptTokens, completionTokens, effectiveLimit int) (int, int, bool) {
	if len(*messages) > 4 && a.CompactionTrigger(promptTokens, completionTokens, effectiveLimit) {
		a.CompressHistory(ctx, messages, sessionID, theme, w)
		recomputed, _ := a.GetGlobalTokens(*messages, allowlist)
		return recomputed, a.GetEffectiveContextLimit(recomputed), true
	}
	return promptTokens, effectiveLimit, false
}

func keepRecentMessageCount(messages []db.Message) int {
	budget := limits.CompactionKeepRecentTokens()
	kept := 0
	used := 0
	for i := len(messages) - 1; i >= 0; i-- {
		cost := EstimateMessageTokens(messages[i])
		if used+cost > budget && kept >= 2 {
			break
		}
		used += cost
		kept++
	}
	for kept < len(messages)-1 && messages[len(messages)-kept].Role == "tool" {
		kept++
	}
	return kept
}

func CompactHistoricalToolOutputs(messages []db.Message) []db.Message {
	if len(messages) == 0 {
		return messages
	}

	lastAssistantIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx <= 0 {
		return messages
	}

	out := make([]db.Message, len(messages))
	copy(out, messages)

	for i := 0; i < lastAssistantIdx; i++ {
		m := out[i]
		if m.Role != "tool" || len(m.Content) <= 1000 || strings.Contains(m.Content, "omitted") || strings.Contains(m.Content, "truncated from historical") {
			continue
		}
		lines := strings.Split(m.Content, "\n")
		if len(lines) > 20 {
			head := strings.Join(lines[:10], "\n")
			tail := strings.Join(lines[len(lines)-5:], "\n")
			m.Content = fmt.Sprintf("%s\n\n[... %d lines omitted from historical '%s' output to preserve context cache; call '%s' again if needed ...]\n\n%s", head, len(lines)-15, m.Name, m.Name, tail)
		} else {
			head := filetool.UTF8SafePrefix(m.Content, 750)
			tail := filetool.UTF8SafeSuffix(m.Content, 250)
			m.Content = head + fmt.Sprintf("\n\n[... output truncated from historical '%s' to preserve context cache ...]\n\n", m.Name) + tail
		}
		out[i] = m
	}

	return out
}

func sortedMapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncateTranscriptSegment(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	return filetool.UTF8SafePrefix(text, maxBytes) + "... (truncated)"
}

func (a *Agent) CompressHistory(
	ctx context.Context,
	messages *[]db.Message,
	sessionID string,
	theme style.UITheme,
	w io.Writer,
) {
	keepMsgCount := keepRecentMessageCount(*messages)
	if keepMsgCount < 2 {
		keepMsgCount = 2
	}

	keepIdx := len(*messages) - keepMsgCount
	if keepIdx <= 1 {
		fmt.Fprintln(w, "conversation is already compact (too few messages to compress).")
		return
	}

	toCompress := (*messages)[1:keepIdx]

	const maxTranscriptChars = 24000
	var transcriptBuilder strings.Builder
	readFilesMap := make(map[string]bool)
	modifiedFilesMap := make(map[string]bool)

	for _, m := range toCompress {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if path := filetool.ExtractFilePath(tc.Function.Arguments); path != "" {
					switch tc.Function.Name {
					case "read":
						readFilesMap[path] = true
					case "write", "edit":
						modifiedFilesMap[path] = true
					}
				}
			}
		}

		if transcriptBuilder.Len() >= maxTranscriptChars {
			continue
		}

		switch m.Role {
		case "system":
			if strings.TrimSpace(m.Content) != "" {
				fmt.Fprintf(&transcriptBuilder, "Prior Context Summary: %s\n\n", truncateTranscriptSegment(m.Content, 2400))
			}
		case "user":
			fmt.Fprintf(&transcriptBuilder, "User: %s\n\n", truncateTranscriptSegment(m.Content, 1200))
		case "assistant":
			if m.Content != "" {
				fmt.Fprintf(&transcriptBuilder, "Agent: %s\n\n", truncateTranscriptSegment(m.Content, 1200))
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&transcriptBuilder, "Agent requested tool call: %s(%s)\n\n", tc.Function.Name, truncateTranscriptSegment(tc.Function.Arguments, 300))
			}
		case "tool":
			fmt.Fprintf(&transcriptBuilder, "Tool Output (%s): %s\n\n", m.Name, truncateTranscriptSegment(m.Content, 400))
		}

		if transcriptBuilder.Len() >= maxTranscriptChars {
			transcriptBuilder.WriteString("[transcript truncated: summarization budget reached]\n")
		}
	}

	readFiles := sortedMapKeys(readFilesMap)
	modifiedFiles := sortedMapKeys(modifiedFilesMap)

	summaryPrompt := fmt.Sprintf(MasterSystemPrompts.CompressionPrompt, transcriptBuilder.String())
	summaryMsgs := []db.Message{{Role: "user", Content: summaryPrompt}}

	infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, infoStyle.Render("[system: context usage threshold reached. compressing older conversation history...]"))

	dummyChan := make(chan StreamChunk, 100)
	drained := make(chan struct{})
	go func() {
		for range dummyChan {
		}
		close(drained)
	}()

	summaryAssistantMsg, err := a.StreamChatCompletions(ctx, summaryMsgs, []string{}, dummyChan)
	close(dummyChan)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}
	if err != nil {
		warnStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s Failed to compress conversation context: %v\n", warnStyle.Render("WARNING:"), err)
		return
	}

	var fileOpsHeader string
	if len(readFiles) > 0 || len(modifiedFiles) > 0 {
		fileOpsHeader = fmt.Sprintf("\n- Files Read: %s\n- Files Modified: %s\n", strings.Join(readFiles, ", "), strings.Join(modifiedFiles, ", "))
	}
	if a != nil {
		if todos := a.GetTodos(); len(todos) > 0 {
			todoParts := make([]string, 0, len(todos))
			for _, t := range todos {
				todoParts = append(todoParts, fmt.Sprintf("[%s] %s: %s", t.Status, t.ID, t.Task))
			}
			fileOpsHeader += fmt.Sprintf("- Active Plan: %s\n", strings.Join(todoParts, " | "))
		}
	}

	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf(domaintool.RuntimeMessage("compression_summary_scaffold", "[System: Below is a summary of the earlier conversation history:%s\n%s]"), fileOpsHeader, summaryAssistantMsg.Content),
	}

	keptMessages := make([]db.Message, 0, len((*messages)[keepIdx:]))
	for _, m := range (*messages)[keepIdx:] {
		m.Content = truncateNonFileToolContent(m.Role, m.Name, m.Content, nonFileToolMaxChars, "... (truncated for context optimization)")
		keptMessages = append(keptMessages, m)
	}

	newMessages := make([]db.Message, 0, 2+len(keptMessages))
	newMessages = append(newMessages, (*messages)[0], summaryMsg)
	newMessages = append(newMessages, keptMessages...)
	newMessages = EnforceToolPairingInvariance(newMessages)

	if sessionID != "" {
		if err := db.RewriteSession(sessionID, newMessages); err != nil {
			a.DebugLogError(sessionID, "db.RewriteSession (after compression)", err)
		}
	}
	*messages = newMessages

	compressedStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	fmt.Fprintf(w, "%s\n\n", compressedStyle.Render(fmt.Sprintf("context compressed · freed %d messages", keepIdx-1)))
}