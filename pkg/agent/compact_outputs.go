package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/db"
	"loop/pkg/storage"
)

// CompactHistoricalToolOutputs reduces old tool output while keeping the newest
// assistant step intact. Only a successful, complete reread can replace a prior
// read; edits and writes do not contain the unchanged portions of a file.
func CompactHistoricalToolOutputs(messages []db.Message) []db.Message {
	lastAssistantIndex := -1
	callsByID := make(map[string]db.ToolCall)
	for index, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		lastAssistantIndex = index
		for _, call := range msg.ToolCalls {
			callsByID[call.ID] = call
		}
	}
	if lastAssistantIndex <= 0 {
		return messages
	}
	output := append([]db.Message(nil), messages...)
	completeReads := make(map[string]bool)
	successfulUpdates := make(map[string]bool)
	for index := lastAssistantIndex - 1; index >= 0; index-- {
		msg := messages[index]
		if msg.Role != "tool" {
			continue
		}
		call, hasCall := callsByID[msg.ToolCallID]
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
		alreadyCompacted := strings.HasPrefix(msg.Content, "[Historical ") ||
			strings.Contains(msg.Content, "omitted from historical") ||
			strings.Contains(msg.Content, "truncated from historical")
		switch {
		case !alreadyCompacted && toolName == "read" && path != "" && !failed && completeReads[path] && len(msg.ContentBlocks) == 0:
			msg.Content = fmt.Sprintf("[Historical read of '%s' superseded by subsequent operation; refer to latest file state]", path)
		case !alreadyCompacted && toolName == "edit" && path != "" && failed && successfulUpdates[path]:
			msg.Content = fmt.Sprintf("[Historical edit attempt on '%s' failed - resolved by subsequent update]", path)
			msg.Recovery = nil
		case !alreadyCompacted && toolName != "ask_user" && len(msg.Content) > 1000:
			msg.Content = boundedHistoricalOutput(msg.Content, msg.SpillPath, 1000)
		}
		output[index] = msg

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
	return msg.IsError || strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Content)), "error:")
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
