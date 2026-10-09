package tool

import (
	"strings"
	"testing"
)

func TestAskUserToolDefault(t *testing.T) {
	tool := NewAskUserTool()
	ctx := &fileTestContext{}

	// Test with recommended option
	args := `{"question": "Which database should we use?", "options": ["SQLite", "PostgreSQL"], "recommended": "PostgreSQL"}`
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "PostgreSQL") {
		t.Fatalf("expected PostgreSQL in output, got: %s", out)
	}

	// Test with structured options objects and no recommended
	args2 := `{"question": "Continue cleanup?", "options": [{"label": "Yes", "description": "Delete files"}, {"label": "No", "description": "Keep files"}]}`
	out2, err2 := tool.Execute(ctx, args2)
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if !strings.Contains(out2, "Yes") {
		t.Fatalf("expected Yes in output, got: %s", out2)
	}
}

func TestAskUserToolCustomHandler(t *testing.T) {
	tool := NewAskUserTool()
	tool.CustomHandler = func(question string, options []AskUserOption, recommended string) (string, error) {
		return "Custom answer: " + recommended, nil
	}

	ctx := &fileTestContext{}
	args := `{"question": "Select framework", "recommended": "Astro"}`
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "Custom answer: Astro" {
		t.Fatalf("unexpected output: %s", out)
	}
}

type mockUserInquirerContext struct {
	fileTestContext
	askedQuestion string
	answeredWith  string
}

func (m *mockUserInquirerContext) AskUser(question string, options []AskUserOption, recommended string) (string, error) {
	m.askedQuestion = question
	return m.answeredWith, nil
}

func TestAskUserToolWithUserInquirer(t *testing.T) {
	tool := NewAskUserTool()
	ctx := &mockUserInquirerContext{
		answeredWith: "No, keep all files",
	}

	args := `{"question": "This workspace has 14,281 files (~437 MB). Proceed with deletion?", "options": ["Yes, delete them", "No, keep all files"], "recommended": "Yes, delete them"}`
	out, err := tool.Execute(ctx, args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "No, keep all files") {
		t.Fatalf("expected user inquirer answer in output, got: %s", out)
	}
	if ctx.askedQuestion != "This workspace has 14,281 files (~437 MB). Proceed with deletion?" {
		t.Fatalf("question mismatch: %s", ctx.askedQuestion)
	}
}
