package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/message"
	"loop/pkg/storage"
)

func historyCompressionEnabled(cfg *config.Config) bool {
	return cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.HistoryCompression), "off")
}

// CompactHistoricalToolOutputs reduces old tool output while keeping the newest
// assistant step intact. Only a successful, complete reread can replace a prior
// read; edits and writes do not contain the unchanged portions of a file.
func CompactHistoricalToolOutputs(messages []db.Message) []db.Message {
	lastAssistantIndex := -1
	for index, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		lastAssistantIndex = index
	}
	if lastAssistantIndex <= 0 {
		return messages
	}
	matches := message.MatchToolResults(messages)
	output := append([]db.Message(nil), messages...)
	completeReads := make(map[string]bool)
	successfulUpdates := make(map[string]bool)
	successfulCommands := make(map[string]bool)
	seenTaskOutput := make(map[string]struct{ body, call string })
	for index := len(messages) - 1; index >= 0; index-- {
		msg := messages[index]
		if msg.Role != "tool" {
			continue
		}
		match, hasCall := matches[index]
		call := match.Call
		toolName := msg.Name
		if toolName == "" && hasCall {
			toolName = call.Function.Name
		}
		toolName = tool.NormalizeName(toolName)
		path := ""
		if hasCall {
			if rawPath := filetool.ExtractFilePath(call.Function.Arguments); rawPath != "" {
				path = filepath.Clean(rawPath)
			}
		}
		failed := historicalToolFailed(msg)
		commandKey := ""
		if msg.Evidence != nil && msg.Evidence.Directory != "" {
			commandKey = msg.Evidence.Directory + "\x00" + call.Function.Arguments
		}
		alreadyCompacted := strings.HasPrefix(msg.Content, "[Historical ") ||
			strings.Contains(msg.Content, "omitted from historical") ||
			strings.Contains(msg.Content, "truncated from historical")
		if toolName == "get_task_status" && !failed && len(msg.ContentBlocks) == 0 && hasCall {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil && args.TaskID != "" {
				if header, body, ok := strings.Cut(msg.Content, "\n\nOutput:\n"); ok && body != "" && !strings.HasPrefix(body, "[New output") && body != "(no new output)" {
					if index < lastAssistantIndex {
						if prior, exists := seenTaskOutput[args.TaskID]; exists && prior.body == body {
							msg.Content = header + "\n\nOutput:\n(unchanged; output retained in " + prior.call + ")"
							alreadyCompacted = true
						}
					}
					if _, exists := seenTaskOutput[args.TaskID]; !exists {
						seenTaskOutput[args.TaskID] = struct{ body, call string }{body, msg.ToolCallID}
					}
				}
			}
		}
		switch {
		case index >= lastAssistantIndex:
			// New results can supersede older reads, but stay intact themselves.
		case !alreadyCompacted && toolName == "read" && path != "" && !failed && completeReads[path] && len(msg.ContentBlocks) == 0:
			msg.Content = fmt.Sprintf("[Historical read of '%s' superseded by subsequent operation; refer to latest file state]", path)
			msg.ContextKind = "superseded"
		case !alreadyCompacted && toolName == "edit" && path != "" && failed && successfulUpdates[path]:
			msg.Content = fmt.Sprintf("[Historical edit attempt on '%s' failed - resolved by subsequent update]", path)
			msg.Recovery = nil
			msg.ContextKind = "resolved_error"
		case !alreadyCompacted && toolName == "bash" && hasCall && commandKey != "" && failed && successfulCommands[commandKey]:
			msg.Content = "[Historical command failure resolved by a later successful execution of the same command; see latest result]"
			msg.ContextKind = "resolved_error"
			msg.Recovery = nil
		case !alreadyCompacted && !failed && storage.IsSpillPath(msg.SpillPath) && toolName != "ask_user" && toolName != "load_skill" && toolName != "read" && toolName != "edit" && toolName != "write" && len(msg.Content) > 1000:
			msg.Content = boundedHistoricalOutput(msg.Content, msg.SpillPath, 1000)
		}
		output[index] = msg
		if toolName == "bash" && hasCall && commandKey != "" && !failed && msg.Evidence != nil && msg.Evidence.ExitCode != nil && *msg.Evidence.ExitCode == 0 && msg.Evidence.Outcome == "succeeded" {
			successfulCommands[commandKey] = true
		}

		// Inspect original output, not the stub we may have just produced.
		if path == "" || failed {
			continue
		}
		switch toolName {
		case "read":
			if isCompleteHistoricalRead(call, messages[index]) {
				completeReads[path] = true
			}
		case "edit", "write":
			if !msg.Evidence.HasErrors() && (msg.Evidence == nil || msg.Evidence.Changed == nil || *msg.Evidence.Changed) {
				successfulUpdates[path] = true
			}
		}
	}
	return output
}

func historicalToolFailed(msg db.Message) bool {
	// Legacy records may predate IsError. Match only an error prefix, since
	// successful source output and diffs commonly contain the word "error".
	return msg.IsError || msg.Evidence.HasErrors() || strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Content)), "error:")
}

func isCompleteHistoricalRead(call db.ToolCall, msg db.Message) bool {
	if historicalToolFailed(msg) || len(msg.ContentBlocks) > 0 || msg.SpillPath != "" || msg.Content == "" {
		return false
	}
	// Require simple file arguments. Unknown wrappers, offsets and explicit
	// limits are conservatively retained rather than guessed to cover a file.
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
		return false
	}
	for key := range args {
		if !filetool.IsFilePathKey(key) {
			return false
		}
	}
	for _, marker := range []string{
		"[Notice:", "[Path '", "Read image file ", "Use read with offset=",
		"byte budget", "[Historical ", "omitted", "truncated",
	} {
		if strings.Contains(msg.Content, marker) {
			return false
		}
	}
	return true
}

// Historical shortening keeps the saved output reference without suggesting
// that a past command or other side effect should be repeated.
func boundedHistoricalOutput(content, spillPath string, budget int) string {
	if len(content) <= budget {
		return content
	}
	notice := "[Earlier output omitted]"
	if storage.IsSpillPath(spillPath) {
		locator := "[Read saved output: " + spillPath + "]"
		if len(locator) <= budget {
			notice = locator
		}
	}
	return filetool.PreviewWithNotice(content, notice, budget)
}
