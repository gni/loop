package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"loop/pkg/domain/message"
)

// Usage reports token consumption metadata from the LLM provider.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ToolCallDelta represents incremental tool call fragments streamed by the model.
type ToolCallDelta struct {
	Index    *int                 `json:"index,omitempty"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function message.ToolFunction `json:"function"`
}

// Delta holds content or reasoning deltas in a streaming chunk.
type Delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
}

// Choice represents a single completion choice candidate.
type Choice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// RawStreamChunk models an OpenAI-compatible SSE chunk payload.
type RawStreamChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// StreamReader consumes and parses SSE data streams.
type StreamReader struct {
	reader *bufio.Reader
}

// NewStreamReader wraps an io.Reader into an SSE stream parser.
func NewStreamReader(r io.Reader) *StreamReader {
	return &StreamReader{reader: bufio.NewReader(r)}
}

// ReadNextChunk reads lines until the next valid data chunk or EOF/[DONE].
// Returns (chunk, isDone, error).
func (sr *StreamReader) ReadNextChunk(ctx context.Context) (*RawStreamChunk, bool, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		default:
		}

		line, err := sr.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("error reading stream line: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil, true, nil
		}

		var chunk RawStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip unparseable chunks or non-JSON comments gracefully
			continue
		}

		return &chunk, false, nil
	}
}
