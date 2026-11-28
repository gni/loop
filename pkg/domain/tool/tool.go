package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// SchemaProp defines the JSON schema property descriptor for tool parameters.
type SchemaProp struct {
	Extra       map[string]json.RawMessage `json:"-"`
	Type        string                     `json:"type,omitempty"`
	Description string                     `json:"description,omitempty"`
	Enum        []string                   `json:"enum,omitempty"`
	Items       *SchemaProp                `json:"items,omitempty"`
	Properties  map[string]SchemaProp      `json:"properties,omitempty"`
	Required    []string                   `json:"required,omitempty"`
}

// JSONSchema describes the parameter validation contract for a tool function.
type JSONSchema struct {
	Extra      map[string]json.RawMessage `json:"-"`
	Type       string                     `json:"type"`
	Properties map[string]SchemaProp      `json:"properties"`
	Required   []string                   `json:"required,omitempty"`
}

var (
	schemaPriorityKeys = []string{"path", "target_file", "targetFile", "file_path", "filePath", "file", "target", "command"}
	todoIDKeys         = []string{"id", "task_id", "taskId", "number", "index", "key"}
	todoDescKeys       = []string{"task", "content", "description", "title", "name", "text", "desc", "summary", "item", "body", "label", "value"}
	todoStatusKeys     = []string{"status", "state"}
)

func isPrioritySchemaKey(key string) bool {
	return slices.Contains(schemaPriorityKeys, key)
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
	return mergeSchema([]byte(fullJSON), j.Extra)
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
	DisableModelInvocation bool   `json:"disable_model_invocation,omitempty"`
	Name                   string `json:"name"`
	Description            string `json:"description"`
	Path                   string `json:"path"`
	Content                string `json:"content"`
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

// BackgroundTaskOptions carries process settings without rewriting shell input.
type BackgroundTaskOptions struct {
	Directory        string
	Environment      map[string]string
	UnsetEnvironment []string
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

// FileMutationRecorder persists recovery intent before a file is committed.
// On success, the caller must release the returned transaction after its write.
type FileMutationRecorder interface {
	BeginFileMutation(context.Context, string, []byte, os.FileMode) (func(), error)
}

type AtomicWriteSettings interface{ IsAtomicWritesEnabled() bool }
type CodeIndexInvalidator interface{ InvalidateCodeIndex(string) }

// ToolExecutor defines the operational contract for executing a tool.
type ToolExecutor interface {
	Name() string
	Definition() Tool
	Execute(ctx AgentContext, arguments string) (string, error)
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
func ExtractMapString(raw map[string]any, keys ...string) string {
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
func ExtractTodoFields(raw map[string]any) (id, task, status string) {
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

	var raw map[string]any
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

// NormalizeTodoStatus canonicalizes common status synonyms and casing.
func NormalizeTodoStatus(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "in_progress", "in-progress", "inprogress", "in progress", "active", "doing", "working", "started", "running":
		return "in_progress", true
	case "completed", "complete", "done", "finished", "success", "closed", "resolved":
		return "completed", true
	case "pending", "todo", "to_do", "to-do", "waiting", "planned", "open", "not_started", "not-started", "":
		return "pending", true
	default:
		return "", false
	}
}

// SplitTodoIDAndTask extracts an optional leading task identifier before ':'
// (for example "task-1: Describe work" or "**step-2**: Fix bug").
// Descriptions without a valid single-token ID prefix return hasID=false so
// colons inside prose (e.g. "Fix pkg/foo.go: nil check") are preserved verbatim.
func SplitTodoIDAndTask(line string) (id, task string, hasID bool) {
	trimmed := strings.TrimSpace(line)
	left, right, ok := strings.Cut(trimmed, ":")
	if !ok {
		return "", trimmed, false
	}
	right = strings.TrimSpace(right)
	if right == "" {
		return "", trimmed, false
	}
	candidate := strings.TrimSpace(left)
	for _, pair := range [][2]string{{"**", "**"}, {"__", "__"}, {"`", "`"}, {"[", "]"}, {"(", ")"}} {
		if strings.HasPrefix(candidate, pair[0]) && strings.HasSuffix(candidate, pair[1]) && len(candidate) > len(pair[0])+len(pair[1]) {
			candidate = strings.TrimSpace(candidate[len(pair[0]) : len(candidate)-len(pair[1])])
		}
	}
	if candidate == "" || len(candidate) > 48 {
		return "", trimmed, false
	}
	for _, r := range candidate {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			return "", trimmed, false
		}
	}
	return candidate, right, true
}

func stripTodoBulletOrNumber(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"- ", "* ", "+ ", "• ", "◦ ", "▪ "} {
		if after, ok := strings.CutPrefix(s, p); ok {
			return strings.TrimSpace(after)
		}
	}
	idx := 0
	for idx < len(s) && s[idx] >= '0' && s[idx] <= '9' {
		idx++
	}
	if idx > 0 && idx < len(s) && (s[idx] == '.' || s[idx] == ')') {
		if idx+1 == len(s) || s[idx+1] == ' ' || s[idx+1] == '\t' {
			return strings.TrimSpace(s[idx+1:])
		}
	}
	return s
}

func stripTodoCheckbox(s string) (status string, rest string, matched bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") {
		return "pending", s, false
	}
	closeIdx := strings.IndexByte(s, ']')
	if closeIdx <= 0 || closeIdx > 16 {
		return "pending", s, false
	}
	token := strings.ToLower(strings.TrimSpace(s[1:closeIdx]))
	tail := strings.TrimSpace(s[closeIdx+1:])
	switch token {
	case "x", "✓", "✔", "done", "completed", "complete":
		return "completed", tail, true
	case ">", "~", "/", "-", "*", "•", "in_progress", "in-progress", "inprogress", "active", "doing", "working":
		return "in_progress", tail, true
	case "", "pending", "todo", "open":
		return "pending", tail, true
	default:
		return "pending", s, false
	}
}

func isMarkdownHeadingOrRule(line string) bool {
	if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
		return true
	}
	if strings.HasPrefix(line, "#") {
		rest := strings.TrimLeft(line, "#")
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	trimmed := strings.TrimSpace(line)
	if len(trimmed) >= 3 && (strings.Trim(trimmed, "-") == "" || strings.Trim(trimmed, "*") == "" || strings.Trim(trimmed, "_") == "") {
		return true
	}
	return false
}

// ParseMarkdownTodoLines parses plain or markdown checklist lines into TodoItems.
// Supports bullets (-, *, +, •), numbered lists (1., 2)), checkboxes ([ ], [x], [>], [/], [-], [~]),
// optional explicit IDs ("task-1: description"), and ignores markdown fences and headings.
func ParseMarkdownTodoLines(raw string, idPrefix string) []TodoItem {
	if idPrefix == "" {
		idPrefix = "task-"
	}
	var parsed []TodoItem
	usedIDs := make(map[string]bool)
	nextAutoID := func() string {
		for n := len(parsed) + 1; ; n++ {
			id := fmt.Sprintf("%s%d", idPrefix, n)
			key := strings.ToLower(id)
			if !usedIDs[key] {
				return id
			}
		}
	}

	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownHeadingOrRule(line) {
			continue
		}
		line = stripTodoBulletOrNumber(line)
		if line == "" {
			continue
		}
		status, rest, hasBox := stripTodoCheckbox(line)
		if hasBox {
			line = rest
		} else {
			// Also handle rare bullet-after-number or checkbox-before-bullet forms.
			status = "pending"
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		id, desc, hasID := SplitTodoIDAndTask(line)
		if !hasID {
			id = nextAutoID()
			desc = line
		} else if usedIDs[strings.ToLower(id)] {
			id = nextAutoID()
		}
		usedIDs[strings.ToLower(id)] = true
		parsed = append(parsed, TodoItem{
			ID:     id,
			Task:   desc,
			Status: status,
		})
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
	Recommended bool   `json:"recommended,omitempty"`
}

// UserInquirer is an optional interface implemented by AgentContext
// to interactively ask the user questions in the terminal/TUI.
type UserInquirer interface {
	AskUser(question string, options []AskUserOption, recommended string) (string, error)
}

func isAskUserOtherLabel(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return lower == "other" || strings.HasPrefix(lower, "other:") || strings.HasPrefix(lower, "other (")
}

// StripRecommendedTag removes a leading or trailing (Recommended) or
// [Recommended] tag (case-insensitive) from an option label.
func StripRecommendedTag(s string) (string, bool) {
	orig := strings.TrimSpace(s)
	if orig == "" {
		return "", false
	}
	cur := orig
	tagged := false
	for {
		lower := strings.ToLower(cur)
		trimmed := false
		for _, tag := range []string{"(recommended)", "[recommended]"} {
			if strings.HasPrefix(lower, tag) {
				rest := strings.TrimLeft(cur[len(tag):], " \t:-")
				if rest != "" {
					cur = strings.TrimSpace(rest)
					tagged = true
					trimmed = true
					break
				}
			}
			if strings.HasSuffix(lower, tag) {
				rest := strings.TrimRight(cur[:len(cur)-len(tag)], " \t:-")
				if rest != "" {
					cur = strings.TrimSpace(rest)
					tagged = true
					trimmed = true
					break
				}
			}
		}
		if !trimmed {
			break
		}
	}
	return cur, tagged
}

// ResolveAskUserRecommendation normalizes option labels and resolves the
// recommended choice from top-level recommended text, per-option flags, or
// inline (Recommended) tags without mutating the caller's slice.
func ResolveAskUserRecommendation(options []AskUserOption, recommended string) ([]AskUserOption, string) {
	normalized := make([]AskUserOption, 0, len(options))
	taggedOther := false
	for _, opt := range options {
		label := strings.TrimSpace(opt.Label)
		desc := strings.TrimSpace(opt.Description)
		if label == "" {
			label = desc
			desc = ""
		}
		cleanLabel, hadTag := StripRecommendedTag(label)
		if cleanLabel == "" {
			continue
		}
		if isAskUserOtherLabel(cleanLabel) {
			if opt.Recommended || hadTag {
				taggedOther = true
			}
			continue
		}
		opt.Label = cleanLabel
		opt.Description = desc
		if hadTag {
			opt.Recommended = true
		}
		normalized = append(normalized, opt)
	}

	recClean, _ := StripRecommendedTag(recommended)
	if recClean != "" {
		if isAskUserOtherLabel(recClean) {
			for i := range normalized {
				normalized[i].Recommended = false
			}
			return normalized, recClean
		}
		matchIdx := -1
		for i, opt := range normalized {
			if strings.EqualFold(opt.Label, recClean) {
				matchIdx = i
				break
			}
		}
		if matchIdx < 0 {
			prefixMatches := 0
			prefixIdx := -1
			recLower := strings.ToLower(recClean)
			for i, opt := range normalized {
				optLower := strings.ToLower(opt.Label)
				if opt.Description != "" &&
					(strings.EqualFold(opt.Label+": "+opt.Description, recClean) ||
						strings.EqualFold(opt.Label+" - "+opt.Description, recClean)) {
					matchIdx = i
					break
				}
				if strings.HasPrefix(optLower, recLower+":") || strings.HasPrefix(optLower, recLower+" - ") {
					prefixMatches++
					prefixIdx = i
				}
			}
			if matchIdx < 0 && prefixMatches == 1 {
				matchIdx = prefixIdx
			}
		}
		if matchIdx >= 0 {
			for i := range normalized {
				normalized[i].Recommended = i == matchIdx
			}
			return normalized, normalized[matchIdx].Label
		}
		return normalized, recClean
	}

	for i, opt := range normalized {
		if opt.Recommended {
			for j := range normalized {
				normalized[j].Recommended = j == i
			}
			return normalized, normalized[i].Label
		}
	}
	if taggedOther {
		return normalized, "Other"
	}
	return normalized, ""
}

// DefaultAskUserSelection resolves the fallback option when interactive prompting is bypassed.
func DefaultAskUserSelection(options []AskUserOption, recommended string) string {
	normalized, resolved := ResolveAskUserRecommendation(options, recommended)
	if resolved != "" {
		return resolved
	}
	if len(normalized) > 0 {
		return normalized[0].Label
	}
	return "Confirmed"
}

// ParseToolArguments unmarshals arguments into dest; if arguments is a plain string
// or quoted JSON string, onString is invoked with that string content.
func ParseToolArguments(arguments string, dest any, onString func(string)) error {
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
