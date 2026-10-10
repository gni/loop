package agent

import (
	"strings"
	"testing"
)

// Streaming case from the real transcript: the model opens its reasoning by echoing
// the prompt in quotes, so the printed stream started with `"` and a stray `.`.
// The filter must consume the echo before anything is written.
func TestPromptEchoFilterSuppressesQuotedEchoAsItStreams(t *testing.T) {
	f := NewPromptEchoFilter("hi")
	chunks := []string{
		`"`,
		`hi`,
		`"`,
		`. `,
		`This is a conversational greeting.`,
		` Keep it concise.`,
	}

	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString(f.Write(chunk))
	}
	out.WriteString(f.Flush())

	got := out.String()
	if !strings.HasPrefix(got, "This is a conversational greeting.") {
		t.Fatalf("echo leaked into the stream: %q", got)
	}
	if strings.HasPrefix(got, `"`) || strings.HasPrefix(got, ".") {
		t.Fatalf("stream still starts with artifact: %q", got)
	}
}

// A leading quote is only suppressed while the echo is still undecided; once the
// model is genuinely thinking, quotes inside the thought must survive.
func TestPromptEchoFilterKeepsQuotesAfterDecision(t *testing.T) {
	f := NewPromptEchoFilter("sup?")
	first := f.Write("This thought contains a \"quoted\" phrase.")
	if first == "" {
		t.Fatal("dropped a real thought")
	}
	if !strings.Contains(first, `"quoted"`) {
		t.Fatalf("lost quotes inside a genuine thought: %q", first)
	}
	if got := f.Write(`"still here"`); got != `"still here"` {
		t.Fatalf("post-decision chunk was filtered: %q", got)
	}
}

// Punctuation, mathematical operators, and decimal points in thoughts must never be dropped.
func TestPromptEchoFilterPreservesPunctuationAndMathInThoughts(t *testing.T) {
	f := NewPromptEchoFilter("hello")
	if got := f.Write("working on the parser"); got != "working on the parser" {
		t.Fatalf("expected real content, got %q", got)
	}
	if got := f.Write("."); got != "." {
		t.Fatalf("period was dropped from thought: %q", got)
	}
	if got := f.Write("\n"); got != "\n" {
		t.Fatalf("whitespace needed for formatting was dropped: %q", got)
	}
	// Check math and decimal tokens:
	mathChunks := []string{"0", ".", "5", " * ", "tmp", "[", "rk", "]", " = ", "(", "1", "/", "2", ")"}
	for _, chunk := range mathChunks {
		if got := f.Write(chunk); got != chunk {
			t.Fatalf("chunk %q was dropped or mutated: %q", chunk, got)
		}
	}
}

// Held text must be released if the stream ends while the echo is undecided.
func TestPromptEchoFilterFlushesUndecidedPrefix(t *testing.T) {
	f := NewPromptEchoFilter("build the parser")
	if got := f.Write("build the pars"); got != "" {
		t.Fatalf("ambiguous prefix should be held, got %q", got)
	}
	// Echo completes here; the following chunk is the first real thought.
	f.Write("er")
	if got := f.Write(" and ship it"); got != "and ship it" {
		t.Fatalf("post-echo text was not emitted: %q", got)
	}

	g := NewPromptEchoFilter("long prompt here")
	g.Write("long prompt her")
	if got := g.Flush(); got != "long prompt her" {
		t.Fatalf("flush lost held text: %q", got)
	}
}

func TestPromptEchoFilterEmptyEchoDoesNotLeakResidueOnFlush(t *testing.T) {
	f := NewPromptEchoFilter("hi")
	chunks := []string{`"`, `hi`, `"`, "\n\n"}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(f.Write(c))
	}
	out.WriteString(f.Flush())
	if got := out.String(); got != "" {
		t.Fatalf("expected empty string for pure echo with residue, got %q", got)
	}
}

func TestPromptEchoFilterThoughtMentioningPromptNotChopped(t *testing.T) {
	f := NewPromptEchoFilter("hi")
	input := `The user said "hi" and ran ls. Just respond briefly.`
	var out strings.Builder
	for _, ch := range input {
		out.WriteString(f.Write(string(ch)))
	}
	out.WriteString(f.Flush())
	if got := out.String(); got != input {
		t.Fatalf("thought mentioning prompt was chopped or altered: got %q, want %q", got, input)
	}
}

func TestPromptEchoFilterSuppressesMarkdownHeadingAndDashVariants(t *testing.T) {
	prompt := "# LLM Harness — Build Plan (v2)\nPlease proceed."
	f := NewPromptEchoFilter(prompt)

	chunks := []string{
		"# ",
		"LLM ",
		"Harness ",
		"— ",
		"Build ",
		"Plan ",
		"(v2)",
		"\n\n",
		"Here is ",
		"the step-by-step ",
		"breakdown.",
	}

	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString(f.Write(chunk))
	}
	out.WriteString(f.Flush())

	got := out.String()
	wantPrefix := "Here is the step-by-step breakdown."
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("heading echo leaked into stream: got %q, want prefix %q", got, wantPrefix)
	}
}

func TestPromptEchoFilterSuppressesLiteralUnicodeDashEscapeInHeading(t *testing.T) {
	prompt := "# LLM Harness — Build Plan (v2)"
	f := NewPromptEchoFilter(prompt)

	chunks := []string{
		"# LLM Harness \\u2014 Build Plan (v2)\n\n",
		"Starting execution now.",
	}

	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString(f.Write(chunk))
	}
	out.WriteString(f.Flush())

	got := out.String()
	if got != "Starting execution now." {
		t.Fatalf("expected echoed title with unicode escape to be suppressed, got %q", got)
	}
}
