package session

import (
	"crypto/rand"
	"fmt"
	"regexp"

	"loop/pkg/domain/message"
)

var sessionIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// ValidateID checks if the provided session identifier contains only allowed characters.
func ValidateID(sessionID string) error {
	if !sessionIDRegex.MatchString(sessionID) {
		return fmt.Errorf("invalid session ID format")
	}
	return nil
}

// NewUUID generates a cryptographically random RFC 4122 v4 UUID string.
func NewUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// SessionInfo represents summary metadata for an archived conversation session.
type SessionInfo struct {
	SessionID   string
	Timestamp   string
	MsgCount    int
	Preview     string
	TotalTokens int
}

// CurrentSessionVersion is the DAG session format version; v1 files carry no header.
const CurrentSessionVersion = 3

// SessionHeader is the versioned first line of a session file.
type SessionHeader struct {
	Type          string `json:"type"` // "session"
	Version       int    `json:"version,omitempty"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	Cwd           string `json:"cwd,omitempty"`
	ParentSession string `json:"parentSession,omitempty"`
}

// Record represents a single JSONL envelope persisted on disk.
// Legacy flat lines omit the DAG fields and load as a linear chain (type
// defaults to "message").
type Record struct {
	ID        string          `json:"id,omitempty"`
	ParentID  string          `json:"parentId,omitempty"`
	Type      string          `json:"type,omitempty"` // "message" or "branch"
	Timestamp string          `json:"timestamp"`
	Message   message.Message `json:"message,omitempty"`
}

// LeafPath walks parent pointers from the leaf (or leafID) back to the root.
// Legacy entries without IDs resolve to a linear chain.
func LeafPath(entries []Record, leafID string) []Record {
	norm := make([]Record, 0, len(entries))
	byID := make(map[string]*Record, len(entries))
	var prevID string
	for i := range entries {
		e := entries[i]
		if e.Type == "session" {
			continue
		}
		if e.ID == "" {
			e = Record{ID: fmt.Sprintf("legacy-%d", i), ParentID: prevID, Type: "message", Timestamp: e.Timestamp, Message: e.Message}
		}
		norm = append(norm, e)
		byID[e.ID] = &norm[len(norm)-1]
		if e.Type != "branch" {
			prevID = e.ID
		}
	}

	leaf := byID[leafID]
	if leaf == nil && len(norm) > 0 {
		leaf = byID[norm[len(norm)-1].ID]
	}
	if leaf == nil {
		return nil
	}
	var path []Record
	cur := leaf
	for cur != nil {
		path = append(path, *cur)
		if cur.ParentID == "" {
			break
		}
		cur = byID[cur.ParentID]
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// Store defines the persistent storage boundary for conversation sessions.
type Store interface {
	SaveMessage(sessionID string, msg message.Message) error
	LoadMessages(sessionID string) ([]message.Message, error)
	RewriteSession(sessionID string, messages []message.Message) error
	ClearSession(sessionID string) error
	ClearHistory() error
	HasMessages(sessionID string) bool
	GetSessions() ([]SessionInfo, error)
	GetSessionsSorted(ascending bool) ([]SessionInfo, error)
	GetLatestSessionID() (string, error)
	SetLatestSessionID(sessionID string) error
	RenameSession(oldID, newID string) error
	// Branch moves the active leaf to entryID by appending a branch marker; history is not rewritten.
	Branch(sessionID, entryID string) error
	// ForkSession creates a new session file with a versioned header recording
	// parentSession and copies the leaf path up to entryID.
	ForkSession(parentSessionID, entryID string) (string, error)
}
