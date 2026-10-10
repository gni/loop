package interactive

import (
	"os"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"loop/pkg/ui/style"
)

func TestAskUserQuestionFreeTextOptionExists(t *testing.T) {
	// The free-write entry must always be appended, even when options are provided.
	var menuOptions []ChoiceMenuOption
	for _, opt := range []string{"keep as-is", "absolute deadline"} {
		menuOptions = append(menuOptions, ChoiceMenuOption{Label: opt, Keys: strings.TrimSpace("1")})
	}
	menuOptions = append(menuOptions, ChoiceMenuOption{
		Label:    "Other: write what you want",
		Keys:     fmt.Sprintf("%d", len(menuOptions)+1),
		FreeText: true,
	})
	if len(menuOptions) != 3 || !menuOptions[2].FreeText {
		t.Fatalf("free-write entry missing: %+v", menuOptions)
	}
}

func TestRunChoiceMenuExFreeText(t *testing.T) {
	theme := style.GetTheme("catppuccin")

	// Menu with a free-text option; user presses key '2' to select it, then types an answer.
	input := strings.NewReader("2\nabsolute cap please\n")
	var out bytes.Buffer

	options := []ChoiceMenuOption{
		{Label: "option one", Keys: "1"},
		{Label: "Other: write what you want", Keys: "2", FreeText: true},
	}

	idx, dismissed, answer := runChoiceMenuEx(input, &out, theme, " which?", options, 0)
	if dismissed {
		t.Fatalf("prompt was dismissed, want selection")
	}
	if idx != 1 {
		t.Fatalf("expected free-text option index 1, got %d", idx)
	}
	if answer != "absolute cap please" {
		t.Fatalf("expected typed answer, got %q", answer)
	}
}

func TestRunChoiceMenuExFreeTextEmptyAnswer(t *testing.T) {
	theme := style.GetTheme("catppuccin")
	input := strings.NewReader("2\n\n")
	var out bytes.Buffer

	options := []ChoiceMenuOption{
		{Label: "option one", Keys: "1"},
		{Label: "Other: write what you want", Keys: "2", FreeText: true},
	}

	idx, dismissed, answer := runChoiceMenuEx(input, &out, theme, " which?", options, 0)
	if dismissed {
		t.Fatalf("prompt was dismissed")
	}
	if idx != 1 || answer != "" {
		t.Fatalf("expected empty answer with free-text index, got idx=%d answer=%q", idx, answer)
	}
}

// TestAskUserQuestionFallbackFreeText exercises the os.Stdin fallback path, but
// runModalChoiceEx opens /dev/tty whenever input is os.Stdin, so redirected stdin
// is ignored and the test blocks on the real terminal. It runs only when
// LOOP_ALLOW_TTY_TESTS=1 is set explicitly.
func TestAskUserQuestionFallbackFreeText(t *testing.T) {
	if os.Getenv("LOOP_ALLOW_TTY_TESTS") == "" {
		t.Skip("opens /dev/tty; set LOOP_ALLOW_TTY_TESTS=1 to run")
	}
	// Simulate: user presses key '4' (free-write entry), then types the answer.
	origStdin := os.Stdin
	tmp, err := os.CreateTemp("", "askuser")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString("4\nabsolute cap please\n"); err != nil {
		t.Fatal(err)
	}
	tmp.Seek(0, 0)
	os.Stdin = tmp

	theme := style.GetTheme("catppuccin")
	var out bytes.Buffer

	answer, err := AskUserQuestion(&out, theme, "which semantics?", []string{"keep window", "absolute deadline", "both"}, "keep window")
	os.Stdin = origStdin

	if err != nil {
		t.Fatal(err)
	}
	if answer != "absolute cap please" {
		t.Fatalf("expected typed answer, got %q", answer)
	}

	// Empty free-write case: key 4, then just Enter.
	tmp2, err := os.CreateTemp("", "askuser2")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmp2.Name())
	if _, err := tmp2.WriteString("4\n\n"); err != nil {
		t.Fatal(err)
	}
	tmp2.Seek(0, 0)
	os.Stdin = tmp2
	answer, err = AskUserQuestion(&out, theme, "which semantics?", []string{"keep window", "absolute deadline", "both"}, "keep window")
	os.Stdin = origStdin
	if err != nil {
		t.Fatal(err)
	}
	if answer != "other: wrote nothing" {
		t.Fatalf("expected 'other: wrote nothing', got %q", answer)
	}
}

func TestRunChoiceMenuExEnterOnHighlightedFreeText(t *testing.T) {
	theme := style.GetTheme("catppuccin")
	// User navigates down to the free-text option with arrow keys, presses Enter,
	// then types the answer.
	input := strings.NewReader("\x1b[B\nkeep absolute cap\n")
	var out bytes.Buffer

	options := []ChoiceMenuOption{
		{Label: "option one", Keys: "1"},
		{Label: "Other: write what you want", Keys: "2", FreeText: true},
	}

	idx, dismissed, answer := runChoiceMenuEx(input, &out, theme, " which?", options, 0)
	if dismissed {
		t.Fatalf("prompt was dismissed")
	}
	if idx != 1 {
		t.Fatalf("expected index 1, got %d", idx)
	}
	if answer != "keep absolute cap" {
		t.Fatalf("expected typed answer via Enter path, got %q", answer)
	}
}
