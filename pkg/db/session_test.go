package db

import (
	"encoding/json"
	"os"
	"testing"

	"loop/pkg/domain/message"
	"loop/pkg/domain/session"
)

func TestEmptySessionsExcludedFromListsAndResume(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("real", Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, record := range []session.Record{
		{Type: "message", Message: Message{Role: "system", Content: "system instructions"}},
		{Type: "settings", Data: json.RawMessage(`{"model":"example"}`)},
		{Type: "context", Messages: []Message{{Role: "system", Content: "instructions"}}},
		{Type: "message", Message: Message{Role: "user", Content: "   "}},
	} {
		if err := store.AppendEntry("empty", record); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := store.GetSessions()
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "real" {
		t.Fatalf("list: %+v, %v", sessions, err)
	}
	latest, err := store.GetLatestSessionID()
	if err != nil || latest != "real" {
		t.Fatalf("latest: %q, %v", latest, err)
	}
	latest, err = store.LatestForWorkspace(store.Workspace)
	if err != nil || latest != "real" {
		t.Fatalf("workspace latest: %q, %v", latest, err)
	}
}

func TestDraftDoesNotReplaceLatestSession(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("real", Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLatestSessionID("unsent-draft"); err != nil {
		t.Fatal(err)
	}
	pointer, err := os.ReadFile(store.latestSessionPath())
	if err != nil || string(pointer) != "real" {
		t.Fatalf("draft replaced latest pointer: %q, %v", pointer, err)
	}
}

func TestAttachmentOnlyConversationIsVisible(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("attachment", Message{Role: "user", ContentBlocks: []message.ContentBlock{{Type: "image", URL: "https://example.com/image.png"}}}); err != nil {
		t.Fatal(err)
	}
	sessions, err := store.GetSessions()
	if err != nil || len(sessions) != 1 {
		t.Fatalf("attachment session hidden: %+v, %v", sessions, err)
	}
}
