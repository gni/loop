package db

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSessionRepairTailPreservesCommittedMessages(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := NewUUID()
	for _, text := range []string{"first", "second"} {
		if err := store.SaveMessage(id, Message{Role: "user", Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(store.dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","id":"interrupted"`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMessages(id); err == nil {
		t.Fatal("damaged tail silently accepted")
	}
	if err := store.RepairTail(id); err != nil {
		t.Fatal(err)
	}
	restored, err := store.LoadMessages(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 2 || restored[0].Content != "first" || restored[1].Content != "second" {
		t.Fatalf("recovery lost committed messages: %+v", restored)
	}
	matches, err := filepath.Glob(path + ".backup-*")
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected one backup of damaged data: %v %v", matches, err)
	}
}
