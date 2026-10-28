package tool

import (
	"encoding/json"

	"loop/pkg/domain/message"
)

type ToolResult struct {
	Output     string                 `json:"output"`
	Content    []message.ContentBlock `json:"content,omitempty"`
	Structured json.RawMessage        `json:"structured_content,omitempty"`
	IsError    bool                   `json:"is_error,omitempty"`
}

type ResultExecutor interface {
	ExecuteResult(AgentContext, string) (ToolResult, error)
}
