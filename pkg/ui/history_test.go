package ui

import (
	"testing"
)

func TestCustomHistory(t *testing.T) {
	hist := NewCustomHistory()

	// Test adding items chronologically (oldest to newest)
	hist.Add("hi")
	hist.Add("sup ?")
	hist.Add("writea full stack api")

	if hist.Len() != 3 {
		t.Errorf("expected length 3, got %d", hist.Len())
	}

	// At(0) should be the newest/most recent
	if hist.At(0) != "writea full stack api" {
		t.Errorf("expected At(0) = 'writea full stack api', got %q", hist.At(0))
	}
	if hist.At(1) != "sup ?" {
		t.Errorf("expected At(1) = 'sup ?', got %q", hist.At(1))
	}
	if hist.At(2) != "hi" {
		t.Errorf("expected At(2) = 'hi', got %q", hist.At(2))
	}

	// Test duplicate entry - adding existing item should remove it from old position and move to front
	hist.Add("sup ?")
	if hist.Len() != 3 {
		t.Errorf("expected length after duplicate move to be 3, got %d", hist.Len())
	}
	if hist.At(0) != "sup ?" {
		t.Errorf("expected At(0) after duplicate add to be 'sup ?', got %q", hist.At(0))
	}
	if hist.At(1) != "writea full stack api" {
		t.Errorf("expected At(1) to be 'writea full stack api', got %q", hist.At(1))
	}
	if hist.At(2) != "hi" {
		t.Errorf("expected At(2) to be 'hi', got %q", hist.At(2))
	}

	// Test duplicate at index 0 - should do nothing and not change history length or order
	hist.Add("sup ?")
	if hist.Len() != 3 {
		t.Errorf("expected length to remain 3, got %d", hist.Len())
	}
	if hist.At(0) != "sup ?" {
		t.Errorf("expected At(0) to remain 'sup ?', got %q", hist.At(0))
	}
}

func TestDeduplicate(t *testing.T) {
	input := []string{"hi", "sup ?", "hi", "writea full stack api", "sup ?"}
	expected := []string{"hi", "writea full stack api", "sup ?"}

	result := Deduplicate(input)
	if len(result) != len(expected) {
		t.Fatalf("expected length %d, got %d", len(expected), len(result))
	}
	for i, val := range result {
		if val != expected[i] {
			t.Errorf("expected index %d to be %q, got %q", i, expected[i], val)
		}
	}
}
