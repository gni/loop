package agent

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"loop/pkg/agent/run"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

func (a *Agent) CompressionThreshold() float64 {
	if a != nil && a.Config != nil && a.Config.CompressionThreshold > 0 {
		return a.Config.CompressionThreshold
	}
	return CompressionThresholdRatio()
}

func CompressionThresholdRatio() float64 {
	return 0.80
}

func CompactionTrigger(promptTokens, completionTokens, effectiveLimit int, ratio ...float64) bool {
	if effectiveLimit <= 0 || promptTokens <= 0 {
		return false
	}
	decision := promptTokens + completionTokens
	// A reserve larger than the local context would request compaction after
	// every tool turn. Scale it down while keeping room for the output floor.
	outputFloor := limits.Current().CompletionTokenFloor
	framingHeadroom := max(64, effectiveLimit/40)
	minimumReserve := min(max(0, effectiveLimit-1), outputFloor+framingHeadroom)
	reserve := min(limits.CompactionReserveTokens(), max(1, effectiveLimit/4))

	r := CompressionThresholdRatio()
	if len(ratio) > 0 && ratio[0] > 0 {
		r = ratio[0]
		ratioHeadroom := int((1.0 - r) * float64(effectiveLimit))
		minSafeHeadroom := min(4096, max(1, effectiveLimit/10))
		if ratioHeadroom < reserve && ratioHeadroom >= minSafeHeadroom {
			reserve = ratioHeadroom
		}
	}

	reserve = max(reserve, minimumReserve)
	return decision >= effectiveLimit-reserve || decision >= int(r*float64(effectiveLimit))
}

func (a *Agent) CompactionTrigger(promptTokens, completionTokens, effectiveLimit int) bool {
	thresh := CompressionThresholdRatio()
	if a != nil {
		thresh = a.CompressionThreshold()
	}
	return CompactionTrigger(promptTokens, completionTokens, effectiveLimit, thresh)
}

func (a *Agent) maybeCompress(ctx context.Context, messages *[]db.Message, sessionID string, theme style.UITheme, w io.Writer, allowlist []string, promptTokens, completionTokens, effectiveLimit int) (int, int, error) {
	if len(*messages) > 4 && a.CompactionTrigger(promptTokens, completionTokens, effectiveLimit) {
		if err := a.CompressHistory(ctx, messages, sessionID, theme, w); err != nil {
			return promptTokens, effectiveLimit, err
		}
		recomputed, limit := a.estimateTurnContext(*messages, allowlist)
		return recomputed, limit, nil
	}
	return promptTokens, effectiveLimit, nil
}

func keepRecentMessageCount(messages []db.Message, budgets ...int) int {
	if len(messages) == 0 {
		return 0
	}
	budget := limits.CompactionKeepRecentTokens()
	if len(budgets) > 0 && budgets[0] > 0 {
		budget = min(budget, budgets[0])
	}
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
	if kept < 2 && len(messages) >= 2 {
		kept = 2
	}
	for kept < len(messages)-1 && messages[len(messages)-kept].Role == "tool" {
		kept++
	}
	return kept
}

func sortedMapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func collectPriorSummaryFiles(content string, readFilesMap, modifiedFilesMap map[string]bool) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		var listStr string
		var target map[string]bool
		if strings.HasPrefix(trimmed, "- Files Read:") {
			listStr = strings.TrimSpace(strings.TrimPrefix(trimmed, "- Files Read:"))
			target = readFilesMap
		} else if strings.HasPrefix(trimmed, "- Files Modified:") {
			listStr = strings.TrimSpace(strings.TrimPrefix(trimmed, "- Files Modified:"))
			target = modifiedFilesMap
		}
		if target == nil || listStr == "" {
			continue
		}
		for _, item := range strings.Split(listStr, ",") {
			if p := strings.TrimSpace(item); p != "" {
				target[p] = true
			}
		}
	}
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
	instructions ...string,
) (compressionErr error) {
	ctx, releaseContext := a.withLifetime(ctx)
	defer releaseContext()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.emit(Event{Type: "compaction_start", SessionID: sessionID})
	defer func() { a.emit(Event{Type: "compaction_end", SessionID: sessionID, IsError: compressionErr != nil}) }()

	keepMsgCount := keepRecentMessageCount(*messages, max(512, a.GetEffectiveContextLimit(0)/3))
	keepIdx := len(*messages) - keepMsgCount
	if keepIdx <= 1 {
		fmt.Fprintln(w, "conversation is already compact (too few messages to compress).")
		return
	}

	toCompress := (*messages)[1:keepIdx]

	transcriptBudget := min(24000, max(2000, a.GetEffectiveContextLimit(0)*2))
	transcript := compactionTranscript(toCompress, transcriptBudget)
	readFilesMap, modifiedFilesMap := observedCompactionFiles(toCompress)

	readFiles := sortedMapKeys(readFilesMap)
	modifiedFiles := sortedMapKeys(modifiedFilesMap)

	summaryPrompt := fmt.Sprintf(domaintool.SystemPromptsSnapshot().CompressionPrompt, transcript)
	if len(instructions) > 0 && strings.TrimSpace(instructions[0]) != "" {
		summaryPrompt += fmt.Sprintf("\n\nUser Instructions for Focus: %s", strings.TrimSpace(instructions[0]))
	}
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

	summaryAssistantMsg, _, _, err := a.StreamChatCompletionsIsolated(run.WithoutTaskInstructions(ctx), summaryMsgs, []string{}, dummyChan)
	close(dummyChan)
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}
	if err == nil && (!hasUsableAssistantResponse(summaryAssistantMsg) || strings.TrimSpace(summaryAssistantMsg.Content) == "" || len(summaryAssistantMsg.ToolCalls) > 0) {
		err = ErrEmptyResponse
	}
	if err != nil {
		compressionErr = err
		warnStyle := style.NewStyle().Foreground(theme.Error).Bold(true)
		fmt.Fprintf(w, "%s Failed to compress conversation context: %v\n", warnStyle.Render("WARNING:"), err)
		return
	}

	var fileOpsParts []string
	if len(readFiles) > 0 {
		fileOpsParts = append(fileOpsParts, fmt.Sprintf("- Files Read: %s", strings.Join(readFiles, ", ")))
	}
	if len(modifiedFiles) > 0 {
		fileOpsParts = append(fileOpsParts, fmt.Sprintf("- Files Modified: %s", strings.Join(modifiedFiles, ", ")))
	}
	var fileOpsHeader string
	if len(fileOpsParts) > 0 {
		fileOpsHeader = "\n" + strings.Join(fileOpsParts, "\n") + "\n"
	}
	if a != nil {
		if todos := run.Plan(ctx, a.GetTodos); len(todos) > 0 {
			todoParts := make([]string, 0, len(todos))
			for _, t := range todos {
				todoParts = append(todoParts, fmt.Sprintf("[%s] %s: %s", t.Status, t.ID, t.Task))
			}
			fileOpsHeader += fmt.Sprintf("- Active Plan: %s\n", strings.Join(todoParts, " | "))
		}
	}

	usage := message.MergeUsage(message.TotalUsage(toCompress), message.TotalUsage([]db.Message{*summaryAssistantMsg}))
	inherited := run.DelegationInstructions(ctx)
	if len(inherited) == 0 {
		inherited = delegationUserInstructions(*messages)
	}
	summaryMsg := db.Message{
		InheritedInstructions: inherited,
		TaskState:             taskSnapshotForHistory(ctx, *messages),
		Usage:                 &usage,
		Role:                  "user",
		ContextKind:           "summary",
		Content:               fmt.Sprintf(domaintool.RuntimeMessage("compression_summary_scaffold", "[System: Below is a summary of the earlier conversation history:%s\n%s]"), fileOpsHeader, summaryAssistantMsg.Content),
	}

	keptMessages := make([]db.Message, 0, len((*messages)[keepIdx:]))
	for _, m := range (*messages)[keepIdx:] {
		m.Content = truncateNonFileToolContent(m, nonFileToolMaxBytes)
		keptMessages = append(keptMessages, m)
	}

	newMessages := make([]db.Message, 0, 2+len(keptMessages))
	newMessages = append(newMessages, (*messages)[0])
	newMessages = append(newMessages, pinnedTaskInstructions(ctx, keptMessages, *messages)...)
	newMessages = append(newMessages, summaryMsg)
	newMessages = append(newMessages, keptMessages...)
	newMessages = EnforceToolPairingInvariance(newMessages)

	if sessionID != "" {
		if err := a.AppendEntryContext(ctx, sessionID, session.Record{Type: "compaction", Messages: newMessages, Message: *summaryAssistantMsg}); err != nil {
			a.recordPersistenceError(err)
			return err
		}
	}
	if a != nil {
		a.MessagesMu.Lock()
	}
	*messages = newMessages
	if a != nil {
		a.MessagesMu.Unlock()
	}

	compressedStyle := style.NewStyle().Foreground(theme.Border).Italic(true)
	fmt.Fprintln(w)
	fmt.Fprintln(w, compressedStyle.Render("[system: context compression complete]"))
	fmt.Fprintf(w, "  %d older messages condensed into 1 summary; %d recent messages retained.\n", len(toCompress), len(keptMessages))
	fmt.Fprintf(w, "  Saved context summary: %d bytes.\n", len(summaryMsg.Content))
	fmt.Fprintf(w, "  File references retained: %d read, %d modified.\n", len(readFiles), len(modifiedFiles))
	fmt.Fprintln(w, "  Summary carried into the next agent turn:")
	fmt.Fprintln(w, CompressionSummaryPreview(summaryMsg.Content))
	fmt.Fprintln(w)
	return
}
