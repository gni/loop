package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
)

func TestSettingsDoNotSaveUnsentSession(t *testing.T) {
	dir := t.TempDir()
	store, err := db.NewJSONLStore(filepath.Join(dir, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	a := agent.NewAgentAt(&config.Config{}, filepath.Join(dir, "config.json"), nil, dir)
	a.Store = store
	defer a.Dispose(context.Background())
	s := &Session{ID: "draft", Store: store, Agent: a, messages: []db.Message{{Role: "system", Content: "instructions"}}}
	// Settings changes and the idle/exit persistence path must both remain in memory.
	if err := s.persistSettings(); err != nil {
		t.Fatal(err)
	}
	if err := s.persistSettingsIfActive(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions", "draft.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("draft saved without input: %v", err)
	}
	s.messages = append(s.messages, db.Message{Role: "user", Content: "hello"})
	for _, msg := range s.messages {
		if err := store.SaveMessage(s.ID, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.persistSettings(); err != nil {
		t.Fatal(err)
	}
	records, err := store.Entries(s.ID)
	if err != nil || records[len(records)-1].Type != "settings" {
		t.Fatalf("real conversation settings not saved: %+v, %v", records, err)
	}
}

func TestOpeningAndClosingWithoutInputDoesNotSaveSession(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Options{Config: &config.Config{}, ConfigPath: filepath.Join(dir, "config.json"), Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateConfig(&config.Config{Model: "changed-before-first-message"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "sessions", "*.jsonl"))
	if err != nil || len(files) != 0 {
		t.Fatalf("empty session files: %v, %v", files, err)
	}
}
