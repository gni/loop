package ui

import (
	"loop/pkg/agent"
)

// SessionDisplayLabel returns the short display label for a session ID and
// whether its persisted display name is already known.
func SessionDisplayLabel(a *agent.Agent, id string) (string, bool) {
	label := id
	if len(label) > 8 {
		label = label[:8]
	}
	if id == "" {
		return "unsaved", true
	}
	name, known := a.SessionNameSnapshot()
	if name != "" {
		label = name
	}
	return label, known
}
