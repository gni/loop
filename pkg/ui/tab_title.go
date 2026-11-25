package ui

import (
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/terminal"
)

var tabTitleSpinnerFrames = [...]string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	tabTitleAnimationInterval = 150 * time.Millisecond
	tabTitleMaxTopicRunes     = 36
)

type tabTitleTracker struct {
	mu        sync.Mutex
	sessionID string
	agentName string
	topic     string
	running   bool
}

var tabTitle tabTitleTracker

// SummarizePromptForTitle extracts a single concise line from a user prompt for display in a terminal tab title.
func SummarizePromptForTitle(prompt string) string {
	for line := range strings.SplitSeq(prompt, "\n") {
		trimmed := strings.Join(strings.Fields(strings.TrimSpace(line)), " ")
		if trimmed == "" {
			continue
		}
		runes := []rune(trimmed)
		if len(runes) > tabTitleMaxTopicRunes {
			return strings.TrimSpace(string(runes[:tabTitleMaxTopicRunes-1])) + "…"
		}
		return trimmed
	}
	return ""
}

func resolveTabSubject(a *agent.Agent, sessionID, agentName, fallbackTopic string) (string, string) {
	subject := strings.TrimSpace(agentName)
	if subject == "" {
		subject = "loop"
	}

	topic := ""
	if a != nil {
		if name, _ := a.SessionNameSnapshot(); strings.TrimSpace(name) != "" {
			topic = strings.TrimSpace(name)
		}
	}
	if topic == "" {
		state := getUI()
		state.StateMu.Lock()
		if state.SessionID == sessionID && state.State.SessionLabel != "" {
			lbl := strings.TrimSpace(state.State.SessionLabel)
			if lbl != "" && lbl != "unsaved" && (sessionID == "" || !strings.HasPrefix(sessionID, lbl)) {
				topic = lbl
			}
		}
		state.StateMu.Unlock()
	}
	if topic == "" && strings.TrimSpace(fallbackTopic) != "" {
		topic = strings.TrimSpace(fallbackTopic)
	}
	if topic == "" && a != nil {
		if ws := strings.TrimSpace(a.WorkspaceRoot); ws != "" {
			base := filepath.Base(ws)
			if base != "" && base != "." && base != string(filepath.Separator) {
				topic = base
			}
		}
	}
	if topic != "" {
		runes := []rune(topic)
		if len(runes) > tabTitleMaxTopicRunes {
			topic = strings.TrimSpace(string(runes[:tabTitleMaxTopicRunes-1])) + "…"
		}
	}
	return subject, topic
}

func formatTabTitle(subject, topic, prefix string) string {
	base := subject
	if topic != "" && !strings.EqualFold(topic, subject) {
		base = subject + " · " + topic
	}
	if prefix != "" {
		return prefix + " " + base
	}
	return base
}

func writeTabTitle(w io.Writer, title string) {
	TerminalMu.Lock()
	terminal.Default().SetTitle(w, title)
	TerminalMu.Unlock()
}

// ResetTabTitleTopic clears any prompt-derived tab title topic when switching or resetting sessions.
func ResetTabTitleTopic() {
	tabTitle.mu.Lock()
	tabTitle.topic = ""
	tabTitle.sessionID = ""
	tabTitle.mu.Unlock()
}

// SetIdleTabTitle updates the terminal tab title to the static agent/session title.
func SetIdleTabTitle(w io.Writer, a *agent.Agent, sessionID, agentName string) {
	tabTitle.mu.Lock()
	if tabTitle.sessionID != sessionID {
		tabTitle.sessionID = sessionID
		tabTitle.topic = ""
	}
	tabTitle.agentName = agentName
	fallbackTopic := tabTitle.topic
	running := tabTitle.running
	tabTitle.mu.Unlock()

	if running {
		return
	}
	subject, topic := resolveTabSubject(a, sessionID, agentName, fallbackTopic)
	writeTabTitle(w, formatTabTitle(subject, topic, ""))
}

// RefreshTabTitle re-renders the current idle tab title (for example after /rename or async session-name load).
func RefreshTabTitle(w io.Writer, a *agent.Agent) {
	TerminalMu.Lock()
	RefreshTabTitleLocked(w, a)
	TerminalMu.Unlock()
}

// RefreshTabTitleLocked re-renders the current idle tab title while the caller already holds TerminalMu.
func RefreshTabTitleLocked(w io.Writer, a *agent.Agent) {
	tabTitle.mu.Lock()
	sessionID := tabTitle.sessionID
	agentName := tabTitle.agentName
	fallbackTopic := tabTitle.topic
	running := tabTitle.running
	tabTitle.mu.Unlock()

	if running {
		return
	}
	if sessionID == "" && a != nil {
		sessionID = a.ActiveSessionID()
	}
	subject, topic := resolveTabSubject(a, sessionID, agentName, fallbackTopic)
	terminal.Default().SetTitle(w, formatTabTitle(subject, topic, ""))
}

// UpdateRunningTabTitlePrompt updates the active tab topic (for example when a steered prompt starts).
func UpdateRunningTabTitlePrompt(prompt string) {
	summary := SummarizePromptForTitle(prompt)
	if summary == "" {
		return
	}
	tabTitle.mu.Lock()
	tabTitle.topic = summary
	tabTitle.mu.Unlock()
}

// StartRunningTabTitle immediately sets an animated terminal tab title for an active agent turn
// and returns a stop function that halts the animation and restores the static idle agent title.
func StartRunningTabTitle(w io.Writer, a *agent.Agent, sessionID, agentName, prompt string) func() {
	summary := SummarizePromptForTitle(prompt)

	tabTitle.mu.Lock()
	if tabTitle.sessionID != sessionID {
		tabTitle.sessionID = sessionID
		tabTitle.topic = ""
	}
	tabTitle.agentName = agentName
	if summary != "" {
		tabTitle.topic = summary
	}
	tabTitle.running = true
	tabTitle.mu.Unlock()

	renderFrame := func(frameIdx int) {
		tabTitle.mu.Lock()
		curTopic := tabTitle.topic
		curAgent := tabTitle.agentName
		curSession := tabTitle.sessionID
		tabTitle.mu.Unlock()

		state := getUI()
		state.StateMu.Lock()
		waiting := state.InApprovalPrompt
		state.StateMu.Unlock()

		subject, topic := resolveTabSubject(a, curSession, curAgent, curTopic)
		prefix := tabTitleSpinnerFrames[frameIdx%len(tabTitleSpinnerFrames)]
		if waiting {
			prefix = "?"
		}
		writeTabTitle(w, formatTabTitle(subject, topic, prefix))
	}

	renderFrame(0)

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(tabTitleAnimationInterval)
		defer ticker.Stop()
		frame := 1
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				renderFrame(frame)
				frame++
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopCh)
			<-doneCh
			tabTitle.mu.Lock()
			tabTitle.running = false
			curSession := tabTitle.sessionID
			curAgent := tabTitle.agentName
			curTopic := tabTitle.topic
			tabTitle.mu.Unlock()
			subject, topic := resolveTabSubject(a, curSession, curAgent, curTopic)
			writeTabTitle(w, formatTabTitle(subject, topic, ""))
		})
	}
}
