package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

// CompactHistoricalToolOutputs condenses verbose tool outputs from completed earlier turns,
// preserving the latest turn's tool outputs in full to protect prefix cache and prevent token explosion.
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

	out := make([]db.Message, len(messages))
	for i, m := range messages {
		// Only compact historical tool outputs that occur before the latest assistant turn
		if m.Role == "tool" && lastAssistantIdx != -1 && i < lastAssistantIdx && len(m.Content) > 1000 {
			msgCopy := m
			lines := strings.Split(m.Content, "\n")
			if len(lines) > 20 {
				head := strings.Join(lines[:10], "\n")
				tail := strings.Join(lines[len(lines)-5:], "\n")
				msgCopy.Content = fmt.Sprintf("%s\n\n[... %d lines omitted from historical '%s' output to preserve context cache; call '%s' again if needed ...]\n\n%s", head, len(lines)-15, m.Name, m.Name, tail)
			} else {
				msgCopy.Content = m.Content[:750] + fmt.Sprintf("\n\n[... output truncated from historical '%s' to preserve context cache ...]\n\n", m.Name) + m.Content[len(m.Content)-250:]
			}
			out[i] = msgCopy
		} else {
			out[i] = m
		}
	}

	return out
}

// CompressHistory summarizes older turns into a compact technical summary message
// when conversation context approaches token limits.
func (a *Agent) CompressHistory(
	ctx context.Context,
	messages *[]db.Message,
	sessionID string,
	theme style.UITheme,
	w io.Writer,
) {
	keepMsgCount := 4
	if len(*messages) <= keepMsgCount+2 {
		keepMsgCount = 2
	}

	keepIdx := len(*messages) - keepMsgCount
	if keepIdx <= 1 {
		fmt.Fprintln(w, "conversation is already compact (too few messages to compress).")
		return
	}

	toCompress := (*messages)[1:keepIdx]

	var transcriptBuilder strings.Builder
	readFilesMap := make(map[string]bool)
	modifiedFilesMap := make(map[string]bool)

	for _, m := range toCompress {
		if m.Role == "user" {
			transcriptBuilder.WriteString(fmt.Sprintf("User: %s\n\n", m.Content))
		} else if m.Role == "assistant" {
			if m.Content != "" {
				transcriptBuilder.WriteString(fmt.Sprintf("Agent: %s\n\n", m.Content))
			}
			if len(m.ToolCalls) > 0 {
				for _, tc := range m.ToolCalls {
					transcriptBuilder.WriteString(fmt.Sprintf("Agent requested tool call: %s(%s)\n\n", tc.Function.Name, tc.Function.Arguments))
					var fileArg struct {
						Path string `json:"path"`
					}
					if json.Unmarshal([]byte(tc.Function.Arguments), &fileArg) == nil && fileArg.Path != "" {
						if tc.Function.Name == "read" {
							readFilesMap[fileArg.Path] = true
						} else if tc.Function.Name == "write" || tc.Function.Name == "edit" {
							modifiedFilesMap[fileArg.Path] = true
						}
					}
				}
			}
		} else if m.Role == "tool" {
			out := m.Content
			if len(out) > 400 {
				out = out[:400] + "... (truncated)"
			}
			transcriptBuilder.WriteString(fmt.Sprintf("Tool Output (%s): %s\n\n", m.Name, out))
		}
	}

	var readFiles, modifiedFiles []string
	for f := range readFilesMap {
		readFiles = append(readFiles, f)
	}
	for f := range modifiedFilesMap {
		modifiedFiles = append(modifiedFiles, f)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)

	transcript := transcriptBuilder.String()
	summaryPrompt := fmt.Sprintf(MasterSystemPrompts.CompressionPrompt, transcript)

	summaryMsgs := []db.Message{
		{
			Role:    "user",
			Content: summaryPrompt,
		},
	}

	infoStyle := style.NewStyle().Foreground(theme.Primary).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, infoStyle.Render("[system: context usage threshold reached. compressing older conversation history...]"))

	// The summarizer has no renderer, so its chunks are discarded. The drain loop
	// must outlive the call and exit when the channel is closed, otherwise every
	// compression leaks one goroutine that holds the channel forever.
	dummyChan := make(chan StreamChunk, 100)
	drained := make(chan struct{})
	go func() {
		for range dummyChan {
			// Discard summarizer stream chunks
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

	summaryText := summaryAssistantMsg.Content
	var fileOpsHeader string
	if len(readFiles) > 0 || len(modifiedFiles) > 0 {
		fileOpsHeader = fmt.Sprintf("\n- Files Read: %s\n- Files Modified: %s\n", strings.Join(readFiles, ", "), strings.Join(modifiedFiles, ", "))
	}
	summaryMsg := db.Message{
		Role:    "system",
		Content: fmt.Sprintf("[System: Below is a summary of the earlier conversation history:%s\n%s]", fileOpsHeader, summaryText),
	}

	keptMessages := make([]db.Message, 0, len((*messages)[keepIdx:]))
	for _, m := range (*messages)[keepIdx:] {
		m.Content = truncateNonFileToolContent(m.Role, m.Name, m.Content, 30000, "... (truncated for context optimization)")
		keptMessages = append(keptMessages, m)
	}

	newMessages := make([]db.Message, 0, 2+len(keptMessages))
	newMessages = append(newMessages, (*messages)[0])  // Keep system prompt
	newMessages = append(newMessages, summaryMsg)      // Add summary
	newMessages = append(newMessages, keptMessages...) // Add latest messages

	if sessionID != "" {
		_ = db.RewriteSession(sessionID, newMessages)
	}
	*messages = newMessages

	compressedStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	fmt.Fprintf(w, "%s\n\n", compressedStyle.Render(fmt.Sprintf("context compressed · freed %d messages", keepIdx-1)))
}
