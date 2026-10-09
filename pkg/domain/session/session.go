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

// Record represents a single JSONL envelope persisted on disk.
type Record struct {
	Timestamp string          `json:"timestamp"`
	Message   message.Message `json:"message"`
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
}
