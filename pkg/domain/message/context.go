package message

import "strings"

// IsShellContext recognizes command records, including older untagged sessions.
// These are user activity, but captured output is not a new user instruction.
func (m Message) IsShellContext() bool {
	if m.Role != RoleUser {
		return false
	}
	if m.ContextKind == "shell" {
		return true
	}
	return m.ContextKind == "" && (strings.HasPrefix(m.Content, "[user manually executed local shell command:") ||
		strings.HasPrefix(m.Content, "[user changed the shell directory for manual commands to:"))
}

// IsContextSummary also recognizes summaries saved before ContextKind existed.
func (m Message) IsContextSummary() bool {
	return m.ContextKind == "summary" || (m.Role == RoleSystem || m.Role == RoleUser) &&
		strings.HasPrefix(m.Content, "[System: Below is a summary of the earlier conversation history:")
}

// IsInternalContext distinguishes provider-compatible context turns from input
// authored by the user. Tool results remain visible execution records.
func (m Message) IsInternalContext() bool {
	if m.Role == RoleSystem {
		return true
	}
	if m.Role != RoleUser {
		return false
	}
	if m.SkillName != "" || m.IsContextSummary() {
		return true
	}
	switch m.ContextKind {
	case "skill", "runtime", "task_context", "system":
		return true
	}
	return false
}

// IsUserInput identifies real prompt turns, including attachment-only input.
func (m Message) IsUserInput() bool {
	return m.Role == RoleUser && m.HasConversationContent()
}
