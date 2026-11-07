package commands

import (
	"bytes"
	"context"
	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
	"path/filepath"
	"strings"
	"testing"

	"loop/pkg/domain/session"
)

func TestResolveEntryID(t *testing.T) {
	entries := []session.Record{
		{ID: "abcd1234efgh"},
		{ID: "abcd5678ijkl"},
		{ID: "xyz987654321"},
	}

	// Exact match
	match, err := resolveEntryID(entries, "abcd1234efgh")
	if err != nil || match != "abcd1234efgh" {
		t.Fatalf("expected exact match 'abcd1234efgh', got %q, err: %v", match, err)
	}

	// Unique prefix match
	match, err = resolveEntryID(entries, "xyz")
	if err != nil || match != "xyz987654321" {
		t.Fatalf("expected prefix match 'xyz987654321', got %q, err: %v", match, err)
	}

	// Ambiguous prefix match
	_, err = resolveEntryID(entries, "abcd")
	if err == nil || !strings.Contains(err.Error(), "ambiguous entry ID prefix") {
		t.Fatalf("expected ambiguous error for prefix 'abcd', got: %v", err)
	}

	// Not found
	_, err = resolveEntryID(entries, "nonexistent")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got: %v", err)
	}

	// Empty target
	_, err = resolveEntryID(entries, "")
	if err == nil || !strings.Contains(err.Error(), "empty entry ID") {
		t.Fatalf("expected empty entry ID error, got: %v", err)
	}
}

func TestResumeLoadsAndRepairsConversation(t *testing.T) {
	dir := t.TempDir()
	store, err := db.NewJSONLStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetDefaultStore(store)
	a := agent.NewAgentAt(&config.Config{}, filepath.Join(dir, "config.json"), nil, dir)
	a.Store = store
	defer a.Dispose(context.Background())
	for _, m := range []db.Message{
		{Role: "system", Content: "saved instructions"},
		{Role: "user", Content: "saved conversation"},
		{Role: "assistant", ToolCalls: []db.ToolCall{{ID: "interrupted-call", Type: "function", Function: db.ToolFunction{Name: "bash", Arguments: `{"command":"pwd"}`}}}},
	} {
		if err := store.SaveMessage("saved-conversation", m); err != nil {
			t.Fatal(err)
		}
	}
	messages := []db.Message{{Role: "user", Content: "previous conversation"}}
	id := "previous"
	theme := style.GetTheme("plain")
	var out bytes.Buffer
	handled, quit := HandleSlashCommand(a, "/resume saved-conversation", &messages, nil, &theme, &out, &id, nil, nil, nil)
	if !handled || quit || id != "saved-conversation" || a.ActiveSessionID() != id {
		t.Fatalf("resume failed: id=%q, output=%s", id, out.String())
	}
	if len(messages) != 4 || messages[1].Content != "saved conversation" || messages[3].ToolCallID != "interrupted-call" || messages[3].ToolOutcome != "unknown" {
		t.Fatalf("conversation not restored/repaired: %+v", messages)
	}
	persisted, err := store.LoadMessages(id)
	if err != nil || len(persisted) != 4 {
		t.Fatalf("repair not persisted: %v, %v", persisted, err)
	}
}

func TestResumeEmptySessionPreservesActiveConversation(t *testing.T) {
	dir := t.TempDir()
	store, err := db.NewJSONLStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetDefaultStore(store)
	a := agent.NewAgentAt(&config.Config{}, filepath.Join(dir, "config.json"), nil, dir)
	a.Store = store
	defer a.Dispose(context.Background())
	if err := store.SaveMessage("empty", db.Message{Role: "system", Content: "instructions"}); err != nil {
		t.Fatal(err)
	}
	id := "current"
	a.SetActiveSessionID(id)
	messages := []db.Message{{Role: "user", Content: "keep this conversation"}}
	if err := loadSavedSession("empty", &id, &messages, a); err == nil {
		t.Fatal("empty session activated")
	}
	if id != "current" || a.ActiveSessionID() != "current" || len(messages) != 1 || messages[0].Content != "keep this conversation" {
		t.Fatalf("failed load changed current conversation: %q, %+v", id, messages)
	}
}
