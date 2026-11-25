package agent

import (
	"context"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
)

// toolProtocol dispatches wire tool definitions, history formatting, prompt
// tool documentation, streaming markup filtering, and response call parsing
// between the JSON (native function calling) and XML (text tag) protocols.
type toolProtocol interface {
	Name() string
	UsesNativeSchemas() bool
	CompactToolSearchResult() bool
	SelectInferenceTools(a *Agent, ctx context.Context, tools []tool.Tool, allowlist []string) []tool.Tool
	PrepareWireTools(tools []tool.Tool, compact bool) []tool.Tool
	PrepareWireMessages(messages []db.Message) []db.Message
	FormatPromptTools(sb *strings.Builder, entries []ToolEntry, toolNames map[string]bool, cfg SystemPromptConfig)
	NewStreamTextFilter(emit func(string), onToolName func(string, int), onToolTarget func(string, int), onToolCall func(string, int)) *FallbackToolTextFilter
	ParseAssistantToolCalls(rawText string, nativeCalls []db.ToolCall) ([]db.ToolCall, string, error)
	CleanReasoning(raw string) string
}

var (
	defaultJSONToolProtocol toolProtocol = jsonToolProtocol{}
	defaultXMLToolProtocol  toolProtocol = xmlToolProtocol{}
)

// toolProtocolFor returns the protocol implementation for a configured name.
// Unknown or empty values fall back to the default JSON protocol.
func toolProtocolFor(name string) toolProtocol {
	if strings.EqualFold(strings.TrimSpace(name), "xml") {
		return defaultXMLToolProtocol
	}
	return defaultJSONToolProtocol
}

func toolProtocolForConfig(cfg *config.Config) toolProtocol {
	if cfg == nil {
		return defaultJSONToolProtocol
	}
	return toolProtocolFor(cfg.ToolProtocol)
}
