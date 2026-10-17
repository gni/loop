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

var (
	schemaPriorityKeys = []string{"path", "target_file", "targetFile", "file_path", "filePath", "file", "target", "command"}
	todoIDKeys         = []string{"id", "task_id", "taskId", "number", "index", "key"}
	todoDescKeys       = []string{"task", "content", "description", "title", "name", "text", "desc", "summary", "item", "body", "label", "value"}
	todoStatusKeys     = []string{"status", "state"}
)

func isPrioritySchemaKey(key string) bool {
	for _, pk := range schemaPriorityKeys {
		if key == pk {
			return true
		}
	}
	return false
}

// MarshalJSON guarantees deterministic property ordering with file paths first.
func (j JSONSchema) MarshalJSON() ([]byte, error) {
	propsParts := make([]string, 0, len(j.Properties))

	for _, key := range schemaPriorityKeys {
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
		if !isPrioritySchemaKey(k) {
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
		orderedReq := make([]string, 0, len(j.Required))
		for _, key := range schemaPriorityKeys {
			for _, rk := range j.Required {
				if rk == key {
					orderedReq = append(orderedReq, rk)
					break
				}
			}
		}
		for _, rk := range j.Required {
			if !isPrioritySchemaKey(rk) {
				orderedReq = append(orderedReq, rk)
			}
		}
		reqBytes, err := json.Marshal(orderedReq)
		if err != nil {
			return nil, err
		}
		requiredJSON = fmt.Sprintf(",%q:%s", "required", string(reqBytes))
	}

	typeName := FirstNonEmpty(j.Type, "object")
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

// MutationPreparer authorizes a path and operation before a large tool payload is generated.
// The mutation tools recheck the authorization against the current filesystem state.
type MutationPreparer interface {
	PrepareMutation(absPath string, isEdit bool) error
	CheckPreparedMutation(absPath string, isEdit bool) error
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

// ExtractMapString returns the first non-empty string representation among keys in raw.
func ExtractMapString(raw map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k]; ok && v != nil {
			if s := strings.TrimSpace(fmt.Sprintf("%v", v)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

// ExtractTodoFields extracts normalized id, description, and status fields from a loosely-typed map.
func ExtractTodoFields(raw map[string]interface{}) (id, task, status string) {
	id = ExtractMapString(raw, todoIDKeys...)
	for _, k := range todoDescKeys {
		if s, ok := raw[k].(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				task = trimmed
				break
			}
		}
	}
	status = ExtractMapString(raw, todoStatusKeys...)
	if status == "" {
		if done, ok := raw["done"].(bool); ok {
			status = boolTodoStatus(done, "completed")
		} else if completed, ok := raw["completed"].(bool); ok {
			status = boolTodoStatus(completed, "completed")
		} else if inProgress, ok := raw["in_progress"].(bool); ok {
			status = boolTodoStatus(inProgress, "in_progress")
		}
	}
	return id, task, status
}

// UnmarshalJSON provides resilient deserialization for TodoItem, accepting
// any common key for task description ("task", "content", "description", "title", etc.)
// or string scalar.
func (ti *TodoItem) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		ti.Task = strings.TrimSpace(s)
		ti.Status = "pending"
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	ti.ID, ti.Task, ti.Status = ExtractTodoFields(raw)
	return nil
}

func boolTodoStatus(flag bool, trueStatus string) string {
	if flag {
		return trueStatus
	}
	return "pending"
}

func stripAnyTodoPrefix(s string, prefixes ...string) (string, bool) {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return strings.TrimSpace(s[len(p):]), true
		}
	}
	return s, false
}

// ParseMarkdownTodoLines parses plain or markdown checklist lines into TodoItems.
func ParseMarkdownTodoLines(raw string, idPrefix string) []TodoItem {
	if idPrefix == "" {
		idPrefix = "task-"
	}
	var parsed []TodoItem
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		status := "pending"
		if rest, ok := stripAnyTodoPrefix(line, "- [x]", "[x]", "- [X]", "[X]"); ok {
			status = "completed"
			line = rest
		} else if rest, ok := stripAnyTodoPrefix(line, "- [>]", "[>]", "- [~]", "[~]", "- [in_progress]", "[in_progress]"); ok {
			status = "in_progress"
			line = rest
		} else if rest, ok := stripAnyTodoPrefix(line, "- [ ]", "[ ]", "• ", "- ", "* "); ok {
			line = rest
		}

		idx := 0
		for idx < len(line) && line[idx] >= '0' && line[idx] <= '9' {
			idx++
		}
		if idx > 0 && idx < len(line) && (line[idx] == '.' || line[idx] == ')') {
			line = strings.TrimSpace(line[idx+1:])
		}
		if line != "" {
			parsed = append(parsed, TodoItem{
				ID:     fmt.Sprintf("%s%d", idPrefix, len(parsed)+1),
				Task:   line,
				Status: status,
			})
		}
	}
	return parsed
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

// DefaultAskUserSelection resolves the fallback option when interactive prompting is bypassed.
func DefaultAskUserSelection(options []AskUserOption, recommended string) string {
	if recommended != "" {
		return recommended
	}
	if len(options) > 0 {
		return options[0].Label
	}
	return "Confirmed"
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
