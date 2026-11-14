package agent

import (
	"fmt"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/db"
)

func isSkillInstruction(msg db.Message) bool {
	if msg.SkillName == "" || msg.IsError {
		return false
	}
	return msg.ContextKind == "skill" && (msg.Role == "user" || msg.Role == "system") ||
		msg.Role == "tool" && tool.NormalizeName(msg.Name) == "load_skill"
}

// Keep only the latest loaded version of each skill, in load order.
func loadedSkillMessages(messages []db.Message) []db.Message {
	seen := make(map[string]bool)
	var loaded []db.Message
	for index := len(messages) - 1; index >= 0; index-- {
		msg := messages[index]
		key := strings.ToLower(msg.SkillName)
		if !isSkillInstruction(msg) || seen[key] {
			continue
		}
		seen[key] = true
		loaded = append(loaded, db.Message{Role: "user", ContextKind: "skill", SkillName: msg.SkillName, Content: msg.Content})
	}
	for left, right := 0, len(loaded)-1; left < right; left, right = left+1, right-1 {
		loaded[left], loaded[right] = loaded[right], loaded[left]
	}
	return loaded
}

// Project tool-loaded skills into pinned instructions without repeating their
// bodies or retaining obsolete call/result pairs. Saved history stays intact.
func skillContextMessages(messages []db.Message) []db.Message {
	loaded := loadedSkillMessages(messages)
	if len(loaded) == 0 {
		return messages
	}
	output := make([]db.Message, 0, len(messages)+len(loaded))
	for _, msg := range messages {
		if isSkillInstruction(msg) {
			if msg.Role != "tool" {
				continue
			}
			msg.Content = fmt.Sprintf("Loaded skill %q; instructions retained in context.", msg.SkillName)
			msg.SkillName = ""
		}
		output = append(output, msg)
	}
	for _, msg := range loaded {
		if len(messages) > 0 && messages[0].Role == "system" && strings.Contains(messages[0].Content, msg.Content) {
			continue
		}
		output = append(output, msg)
	}
	return output
}

func pinnedSkillInstructions(source, kept []db.Message) []db.Message {
	present := make(map[string]bool)
	for _, msg := range kept {
		if isSkillInstruction(msg) {
			present[strings.ToLower(msg.SkillName)] = true
		}
	}
	var pinned []db.Message
	for _, msg := range loadedSkillMessages(source) {
		if !present[strings.ToLower(msg.SkillName)] {
			pinned = append(pinned, msg)
		}
	}
	return pinned
}
