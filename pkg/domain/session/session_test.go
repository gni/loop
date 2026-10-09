package session

import (
	"encoding/json"
	"strings"
	"testing"

	"loop/pkg/domain/message"
)

func TestValidateID(t *testing.T) {
	validIDs := []string{
		"session-1",
		"abc_123",
		"8b1935e4-2395-46aa-b2b5-520e5ef56cf9",
	}
	for _, id := range validIDs {
		if err := ValidateID(id); err != nil {
			t.Errorf("expected valid ID for %q, got error: %v", id, err)
		}
	}

	invalidIDs := []string{
		"",
		"session with spaces",
		"../../hack",
		"bad$id",
		"semi;colon",
	}
	for _, id := range invalidIDs {
		if err := ValidateID(id); err == nil {
			t.Errorf("expected error for invalid ID %q, got nil", id)
		}
	}
}

func TestNewUUID(t *testing.T) {
	id1 := NewUUID()
	id2 := NewUUID()

	if len(id1) != 36 {
		t.Fatalf("expected UUID length 36, got %d (%s)", len(id1), id1)
	}
	if id1 == id2 {
		t.Fatal("expected distinct UUIDs")
	}
	if err := ValidateID(id1); err != nil {
		t.Fatalf("expected generated UUID to be valid ID, got: %v", err)
	}
	parts := strings.Split(id1, "-")
	if len(parts) != 5 {
		t.Fatalf("expected 5 segments in UUID, got %d", len(parts))
	}
}

func TestRecordSerialization(t *testing.T) {
	rec := Record{
		Timestamp: "2026-10-09 14:00:00",
		Message:   message.NewUser("hello world"),
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("failed to marshal record: %v", err)
	}

	var parsed Record
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal record: %v", err)
	}

	if parsed.Timestamp != rec.Timestamp || parsed.Message.Content != "hello world" {
		t.Fatalf("unexpected parsed record: %+v", parsed)
	}
}
