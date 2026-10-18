package llm

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"loop/pkg/domain/message"
)

// Usage reports token consumption metadata from the LLM provider, including
// prompt-cache read/write counts when the provider reports them.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CacheReadTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// Timings reports llama.cpp token generation counts when usage is omitted.
type Timings struct {
	PromptN    int `json:"prompt_n"`
	PredictedN int `json:"predicted_n"`
}

// ApplyPromptCacheBreakpoints marks the system prompt and the tool-catalog prefix
// (the message block that carries the catalog, i.e. the first system turn) with an
// ephemeral cache breakpoint. Call this only for endpoints that explicitly
// support cache_control in OpenAI-compatible chat messages.
func ApplyPromptCacheBreakpoints(messages []message.Message) {
	for i, m := range messages {
		if m.IsSystem() {
			hash := sha256.Sum256([]byte(m.Content))
			messages[i].CacheControl = []message.CacheBlock{{Type: message.CacheControlEphemeral, Key: fmt.Sprintf("%x", hash[:])}}
			break
		}
	}
}

// ParseUsage extracts usage (including cache read/write counts) from a raw SSE
// data payload without disturbing the existing chunk parser.
func ParseUsage(data string) (*Usage, error) {
	var chunk RawStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil, err
	}
	return chunk.Usage, nil
}

// ToolCallDelta represents incremental tool call fragments streamed by the model.
type ToolCallDelta = message.ToolCall

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
	ID      string          `json:"id"`
	Object  string          `json:"object"`
	Created int64           `json:"created"`
	Model   string          `json:"model"`
	Choices []Choice        `json:"choices"`
	Usage   *Usage          `json:"usage,omitempty"`
	Timings *Timings        `json:"timings,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// StreamReader consumes and parses SSE data streams.
type StreamReader struct {
	reader     *bufio.Reader
	OnActivity func()
	eof        bool
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
		if sr.eof {
			return nil, true, nil
		}

		line, err := sr.reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				return nil, false, fmt.Errorf("error reading stream: %w", err)
			}
			if line == "" {
				return nil, true, nil
			}
			sr.eof = true
		}

		if sr.OnActivity != nil {
			sr.OnActivity()
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return nil, true, nil
		}
		if data == "" {
			continue
		}

		var chunk RawStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, false, fmt.Errorf("invalid stream data: %w", err)
		}

		return &chunk, false, nil
	}
}
