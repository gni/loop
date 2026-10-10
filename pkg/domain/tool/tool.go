package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SchemaProp defines the JSON schema property descriptor for tool parameters.
type SchemaProp struct {
	Type        string                `json:"type"`
	Description string                `json:"description,omitempty"`
	Enum        []string              `json:"enum,omitempty"`
	Items       *SchemaProp           `json:"items,omitempty"`
	Properties  map[string]SchemaProp `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
}

// JSONSchema describes the parameter validation contract for a tool function.
type JSONSchema struct {
	Type       string                `json:"type"`
	Properties map[string]SchemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

// MarshalJSON guarantees deterministic property ordering with file paths first.
func (j JSONSchema) MarshalJSON() ([]byte, error) {
	var propsParts []string

	orderedKeys := []string{"path", "target_file", "targetFile", "file_path", "filePath", "file", "target", "command"}
	for _, key := range orderedKeys {
		if prop, ok := j.Properties[key]; ok {
			propBytes, err := json.Marshal(prop)
			if err != nil {
				return nil, err
			}
			propsParts = append(propsParts, fmt.Sprintf("%q:%s", key, string(propBytes)))
		}
	}

	var otherKeys []string
	for k := range j.Properties {
		isOrdered := false
		for _, ok := range orderedKeys {
			if k == ok {
				isOrdered = true
				break
			}
		}
		if !isOrdered {
			otherKeys = append(otherKeys, k)
		}
	}
	sort.Strings(otherKeys)

	for _, k := range otherKeys {
		propBytes, err := json.Marshal(j.Properties[k])
		if err != nil {
			return nil, err
		}
		propsParts = append(propsParts, fmt.Sprintf("%q:%s", k, string(propBytes)))
	}

	propsJSON := "{" + strings.Join(propsParts, ",") + "}"

	var requiredJSON string
	if len(j.Required) > 0 {
		var orderedReq []string
		for _, key := range orderedKeys {
			for _, rk := range j.Required {
				if rk == key {
					orderedReq = append(orderedReq, rk)
					break
				}
			}
		}
		for _, rk := range j.Required {
			isOrdered := false
			for _, ok := range orderedKeys {
				if rk == ok {
					isOrdered = true
					break
				}
			}
			if !isOrdered {
				orderedReq = append(orderedReq, rk)
			}
		}
		reqBytes, err := json.Marshal(orderedReq)
		if err != nil {
			return nil, err
		}
		requiredJSON = fmt.Sprintf(",%q:%s", "required", string(reqBytes))
	}

	typeName := j.Type
	if typeName == "" {
		typeName = "object"
	}

	fullJSON := fmt.Sprintf("{%q:%q,%q:%s%s}", "type", typeName, "properties", propsJSON, requiredJSON)
	return []byte(fullJSON), nil
}

// FunctionDefinition specifies the tool name, description, and parameter schema.
type FunctionDefinition struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  JSONSchema `json:"parameters"`
}

// Tool describes an OpenAI/OpenAPI compatible tool declaration.
type Tool struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

// Skill represents domain instructions and operational guides injected into context.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
}

// AgentContext exposes workspace boundaries, skill state, and task spawning to tools.
type AgentContext interface {
	SafePath(inputPath string) (string, error)
	GetWorkspaceRoot() string
	GetActiveSkills() []Skill
	ReloadSkills() []Skill
	SpawnTask(command string, w io.Writer) (string, error)
	GetTaskStatus(taskID string) (string, string, error)
	KillTask(taskID string) error
	Context() context.Context
	HasSubagent(name string) bool
}

// LiveOutputWriter allows streaming tools (such as bash) to stream output directly to terminal.
type LiveOutputWriter interface {
	GetLiveWriter() io.Writer
	SetLiveBodyStreamed(bool)
	DidStreamLiveBody() bool
}

// FileObserver enforces read-before-edit and compare-and-swap checks on filesystem tools.
type FileObserver interface {
	RecordRead(absPath string, data []byte)
	CheckMutationAllowed(absPath string, isEdit bool) error
	RecordMutation(absPath string, data []byte)
}

// ToolExecutor defines the operational contract for executing a tool.
type ToolExecutor interface {
	Name() string
	Definition() Tool
	Execute(ctx AgentContext, arguments string) (string, error)
}

// PromptContributor contributes prompt snippets and operational guidelines to system instructions.
type PromptContributor interface {
	PromptSnippet() string
	PromptGuidelines() []string
}

// GetPromptSnippet retrieves the prompt snippet for a tool executor.
func GetPromptSnippet(t ToolExecutor) string {
	if pc, ok := t.(interface{ PromptSnippet() string }); ok {
		if s := pc.PromptSnippet(); s != "" {
			return s
		}
	}
	return t.Definition().Function.Description
}

// GetPromptGuidelines retrieves the prompt guidelines for a tool executor.
func GetPromptGuidelines(t ToolExecutor) []string {
	if pc, ok := t.(interface{ PromptGuidelines() []string }); ok {
		return pc.PromptGuidelines()
	}
	return nil
}

// TodoItem represents an individual task in the agent's plan.
type TodoItem struct {
	ID     string `json:"id"`
	Task   string `json:"task"`
	Status string `json:"status"` // "pending", "in_progress", "completed"
}

// TodoState is an interface implemented by agent contexts that maintain a task board.
type TodoState interface {
	GetTodos() []TodoItem
	SetTodos(todos []TodoItem) error
}

// AskUserOption represents a selectable choice presented to the user.
type AskUserOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// UserInquirer is an optional interface implemented by AgentContext
// to interactively ask the user questions in the terminal/TUI.
type UserInquirer interface {
	AskUser(question string, options []AskUserOption, recommended string) (string, error)
}

// ParseToolArguments unmarshals arguments into dest; if arguments is a plain string
// or quoted JSON string, onString is invoked with that string content.
func ParseToolArguments(arguments string, dest interface{}, onString func(string)) error {
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			if onString != nil {
				onString(unquoted)
			}
			return nil
		}
	}
	if err := json.Unmarshal([]byte(arguments), dest); err != nil {
		if !strings.HasPrefix(trimmed, "{") && trimmed != "" && onString != nil {
			onString(trimmed)
			return nil
		}
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// FirstNonEmpty returns the first non-empty string among the given values.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewFunctionTool creates a standard domain tool definition.
func NewFunctionTool(name, description string, props map[string]SchemaProp, required ...string) Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        name,
			Description: description,
			Parameters: JSONSchema{
				Type:       "object",
				Properties: props,
				Required:   required,
			},
		},
	}
}

// StringProp creates a string type schema property descriptor.
func StringProp(description string) SchemaProp {
	return SchemaProp{Type: "string", Description: description}
}

// NumberProp creates a number type schema property descriptor.
func NumberProp(description string) SchemaProp {
	return SchemaProp{Type: "number", Description: description}
}

// BoolProp creates a boolean type schema property descriptor.
func BoolProp(description string) SchemaProp {
	return SchemaProp{Type: "boolean", Description: description}
}

// IsSafeIdentifier reports whether s contains only alphanumeric, underscore, or hyphen characters.
func IsSafeIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

