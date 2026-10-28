package db

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"loop/pkg/domain/session"
)

func TestContextProjectionPreservesTranscriptAndBranchIDs(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("test", Message{Role: "user", Content: "original"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries("test")
	if err != nil {
		t.Fatal(err)
	}
	root := entries[len(entries)-1].ID
	if err := store.RewriteSession("test", []Message{{Role: "system", Content: "summary"}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.sessionPath("test"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "original") {
		t.Fatal("projection destroyed raw history")
	}
	messages, err := store.LoadMessages("test")
	if err != nil || len(messages) != 1 || messages[0].Content != "summary" {
		t.Fatalf("projection: %+v %v", messages, err)
	}
	if err := store.Branch("test", root); err != nil {
		t.Fatal(err)
	}
	messages, err = store.LoadMessages("test")
	if err != nil || len(messages) != 1 || messages[0].Content != "original" {
		t.Fatalf("branch lost original: %+v %v", messages, err)
	}
	if err := store.Branch("test", "missing"); err == nil {
		t.Fatal("missing branch target succeeded")
	}
	if _, err := store.ForkSession("test", "missing"); err == nil {
		t.Fatal("missing fork target succeeded")
	}
}

func TestCorruptAndFutureSessionsFail(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.sessionPath("bad"), []byte("{broken}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMessages("bad"); err == nil {
		t.Fatal("corrupt session was accepted")
	}
	if _, err := store.Import(bytes.NewBufferString(`{"type":"session","version":999,"id":"test"}`)); err == nil {
		t.Fatal("future version was accepted")
	}
	if err := session.ValidateEntries([]session.Record{{ID: "a", ParentID: "a"}}); err == nil {
		t.Fatal("cycle was accepted")
	}
}

func TestImportPreservesAbandonedBranchesAndRepairsOnlyTail(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("original", Message{Role: "user", Content: "root"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries("original")
	if err != nil {
		t.Fatal(err)
	}
	root := entries[len(entries)-1].ID
	if err := store.SaveMessage("original", Message{Role: "assistant", Content: "abandoned"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Branch("original", root); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage("original", Message{Role: "user", Content: "selected"}); err != nil {
		t.Fatal(err)
	}
	var exported bytes.Buffer
	if err := store.Export("original", &exported); err != nil {
		t.Fatal(err)
	}
	imported, err := store.Import(&exported)
	if err != nil {
		t.Fatal(err)
	}
	importedEntries, err := store.Entries(imported)
	if err != nil {
		t.Fatal(err)
	}
	entries, err = store.Entries("original")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(importedEntries) {
		t.Fatal("import dropped branches")
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].ID != importedEntries[i].ID {
			t.Fatal("import changed entry identity")
		}
	}
	messages, err := store.LoadMessages(imported)
	if err != nil || len(messages) != 2 || messages[1].Content != "selected" {
		t.Fatalf("wrong imported projection: %+v %v", messages, err)
	}
	file, err := os.OpenFile(store.sessionPath(imported), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(`{"truncated"`)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := store.RepairTail(imported); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadMessages(imported); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.sessionPath("interior"), []byte("{bad}\n{bad-tail"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RepairTail("interior"); err == nil {
		t.Fatal("interior corruption was repaired silently")
	}
}
