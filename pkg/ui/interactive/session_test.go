package interactive

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestSessionPickerLoadsSelectedConversation(t *testing.T) {
	if err := db.InitDB(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"conversation-one", "conversation-two"} {
		if err := db.SaveMessage(id, db.Message{Role: "user", Content: "message in " + id}); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := db.GetSessions()
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %v, %v", sessions, err)
	}
	sr := NewSessionReader(bytes.NewBufferString("\x1b[B\r"))
	defer sr.Close()
	id, fresh, err := runSessionPicker(sr, nil, io.Discard, style.GetTheme("plain"))
	if err != nil || fresh || id != sessions[1].SessionID {
		t.Fatalf("got %q, new=%v, err=%v", id, fresh, err)
	}
	messages, err := db.LoadMessages(id)
	if err != nil || len(messages) != 1 || messages[0].Content != "message in "+id {
		t.Fatalf("wrong conversation: %v, %v", messages, err)
	}
}

func TestSessionPickerCancelAndNew(t *testing.T) {
	if err := db.InitDB(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		input string
		fresh bool
		err   error
	}{
		{"\r", true, nil}, {"\x1b", false, ErrSelectionCancelled}, {"", false, io.EOF},
	} {
		sr := NewSessionReader(bytes.NewBufferString(tt.input))
		id, fresh, err := runSessionPicker(sr, nil, io.Discard, style.GetTheme("plain"))
		sr.Close()
		if id != "" || fresh != tt.fresh || !errors.Is(err, tt.err) {
			t.Fatalf("input %q: %q, %v, %v", tt.input, id, fresh, err)
		}
	}
}
