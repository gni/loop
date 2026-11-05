package commands

import (
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
