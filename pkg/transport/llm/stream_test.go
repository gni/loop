package llm

import (
	"context"
	"strings"
	"testing"
)

func TestStreamReaderParsing(t *testing.T) {
	sseData := `
data: {"id":"1","choices":[{"delta":{"content":"Hello"}}]}

data: {"id":"2","choices":[{"delta":{"content":" world!"}}]}
data: {"id":"3","usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}
data: [DONE]
`
	reader := NewStreamReader(strings.NewReader(sseData))

	// Chunk 1
	c1, done, err := reader.ReadNextChunk(context.Background())
	if err != nil || done || c1 == nil || c1.Choices[0].Delta.Content != "Hello" {
		t.Fatalf("chunk 1 failed: %v, done=%v, c1=%+v", err, done, c1)
	}

	// Chunk 2
	c2, done, err := reader.ReadNextChunk(context.Background())
	if err != nil || done || c2 == nil || c2.Choices[0].Delta.Content != " world!" {
		t.Fatalf("chunk 2 failed: %v, done=%v, c2=%+v", err, done, c2)
	}

	// Chunk 3 (Usage)
	c3, done, err := reader.ReadNextChunk(context.Background())
	if err != nil || done || c3 == nil || c3.Usage == nil || c3.Usage.TotalTokens != 12 {
		t.Fatalf("chunk 3 failed: %v, done=%v, c3=%+v", err, done, c3)
	}

	// Chunk 4 ([DONE])
	c4, done, err := reader.ReadNextChunk(context.Background())
	if err != nil || !done || c4 != nil {
		t.Fatalf("expected done, got: err=%v, done=%v, c4=%+v", err, done, c4)
	}
}

func TestThinkingStreamFilterSplitTag(t *testing.T) {
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder

	filter := NewThinkingStreamFilter(
		func(c string) { contentBuilder.WriteString(c) },
		func(r string) { reasoningBuilder.WriteString(r) },
	)

	// Stream chunks that split the tags across boundaries
	chunks := []string{
		"Start ",
		"<thi",
		"nk>This is deep ",
		"thought</thi",
		"nk> End.",
	}

	for _, chunk := range chunks {
		filter.Feed(chunk)
	}
	filter.Flush()

	expectedContent := "Start  End."
	expectedReasoning := "This is deep thought"

	if contentBuilder.String() != expectedContent {
		t.Errorf("content mismatch: got %q, want %q", contentBuilder.String(), expectedContent)
	}
	if reasoningBuilder.String() != expectedReasoning {
		t.Errorf("reasoning mismatch: got %q, want %q", reasoningBuilder.String(), expectedReasoning)
	}
}

func TestThinkingStreamFilterChannelTag(t *testing.T) {
	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder

	filter := NewThinkingStreamFilter(
		func(c string) { contentBuilder.WriteString(c) },
		func(r string) { reasoningBuilder.WriteString(r) },
	)

	filter.Feed("Lead <|channel>thoughtinner reasoning<channel|> Trail")
	filter.Flush()

	if contentBuilder.String() != "Lead  Trail" {
		t.Errorf("content mismatch: got %q, want %q", contentBuilder.String(), "Lead  Trail")
	}
	if reasoningBuilder.String() != "inner reasoning" {
		t.Errorf("reasoning mismatch: got %q, want %q", reasoningBuilder.String(), "inner reasoning")
	}
}
