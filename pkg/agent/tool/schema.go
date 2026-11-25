package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/diagnostic"
	domaintool "loop/pkg/domain/tool"
)

type compiledSchema struct {
	definition string
	schema     *jsonschema.Schema
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema reference is not registered: %s", url)
}

func (r *ToolRegistry) Prepare(name, arguments string) (ToolExecutor, string, error) {
	if r.IsDisabled(name) {
		return nil, "", fmt.Errorf("tool %q is disabled", name)
	}
	executor, ok := r.Lookup(name)
	if !ok && r.UnknownToolHook != nil {
		executor, ok = r.UnknownToolHook(name)
	}
	if !ok {
		return nil, "", fmt.Errorf("unknown tool: %s", name)
	}
	if len(arguments) > 8<<20 {
		return nil, "", fmt.Errorf("tool arguments exceed 8 MiB")
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(arguments))
	if err != nil {
		return nil, "", &diagnostic.Error{Code: diagnostic.InvalidArguments, Target: name, Cause: fmt.Errorf("invalid JSON tool arguments for %q: %w", name, err)}
	}
	if err := validateArgumentNumbers(value); err != nil {
		return nil, "", fmt.Errorf("tool %q arguments: %w", name, err)
	}
	value, err = normalizeToolArguments(executor.Name(), arguments, value)
	if err != nil {
		return nil, "", &diagnostic.Error{Code: diagnostic.InvalidArguments, Target: name, Cause: fmt.Errorf("tool %q arguments: %w", name, err)}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	definition, err := json.Marshal(executor.Definition().Function.Parameters)
	if err != nil {
		return nil, "", fmt.Errorf("tool %q schema: %w", name, err)
	}
	r.mu.Lock()
	cached, exists := r.schemas[executor.Name()]
	if !exists || cached.definition != string(definition) {
		compiler := jsonschema.NewCompiler()
		compiler.DefaultDraft(jsonschema.Draft2020)
		compiler.UseLoader(localSchemaLoader{})
		data, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(definition))
		if decodeErr == nil {
			decodeErr = compiler.AddResource("https://loop.invalid/tool.json", data)
		}
		if decodeErr == nil {
			cached.schema, decodeErr = compiler.Compile("https://loop.invalid/tool.json")
		}
		if decodeErr != nil {
			r.mu.Unlock()
			return nil, "", fmt.Errorf("tool %q schema: %w", name, decodeErr)
		}
		cached.definition = string(definition)
		if r.schemas == nil {
			r.schemas = make(map[string]compiledSchema)
		}
		r.schemas[executor.Name()] = cached
	}
	r.mu.Unlock()
	if err := cached.schema.Validate(value); err != nil {
		return nil, "", &diagnostic.Error{Code: diagnostic.InvalidArguments, Target: name, Cause: fmt.Errorf("tool %q arguments: %w", name, err)}
	}
	return executor, string(encoded), nil
}

// Keep the existing finite numeric range while preserving exact JSON values.
// Reject underflow as well: treating a tiny nonzero value as zero changes the
// requested operation and can overflow exact arithmetic in schema validation.
func validateArgumentNumbers(value any) error {
	switch value := value.(type) {
	case json.Number:
		number, err := value.Float64()
		mantissa := value.String()
		if end := strings.IndexAny(mantissa, "eE"); end >= 0 {
			mantissa = mantissa[:end]
		}
		if err != nil || number == 0 && strings.ContainsAny(mantissa, "123456789") {
			return fmt.Errorf("numeric argument is outside the supported range")
		}
	case map[string]any:
		for _, item := range value {
			if err := validateArgumentNumbers(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := validateArgumentNumbers(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeToolArguments(name, arguments string, value any) (any, error) {
	name = NormalizeName(name)
	if name == "todo" {
		return normalizeTodoArguments(value), nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	copyAlias := func(target string, aliases ...string) {
		if _, exists := object[target]; exists {
			return
		}
		for _, alias := range aliases {
			if v, exists := object[alias]; exists {
				object[target] = v
				return
			}
		}
	}
	switch name {
	case "write":
		path, content, err := filetool.ParseWriteArguments(arguments)
		if err != nil {
			return value, err
		}
		return map[string]any{"path": path, "content": content}, nil
	case "edit":
		path, edits, err := filetool.ParseEditArguments(arguments)
		if err != nil {
			return value, err
		}
		updates := make([]any, len(edits))
		for i, edit := range edits {
			update := map[string]any{"oldText": edit.OldText, "newText": edit.NewText}
			if edit.Path != "" && edit.Path != path {
				update["path"] = edit.Path
			}
			updates[i] = update
		}
		// Keep only the canonical request, including per-edit paths.
		return map[string]any{"path": path, "updates": updates}, nil
	case "read", "list_directory":
		path, _ := object["path"].(string)
		if path = filetool.ExtractFilePath(arguments, path); path != "" {
			object["path"] = path
		}
	case "find_files":
		copyAlias("pattern", "glob")
		path, _ := object["path"].(string)
		if path = filetool.ExtractFilePath(arguments, path); path != "" {
			object["path"] = path
		}
	case "bash":
		copyAlias("command", "cmd", "arguments")
		copyAlias("dir", "cwd")
	case "get_task_status", "stop_task":
		copyAlias("task_id", "id", "task", "taskId")
	case "load_skill":
		copyAlias("name", "skill", "skill_name")
	case "load_mcp":
		copyAlias("name", "server")
		if _, ok := object["name"]; !ok {
			object["name"] = ""
		}
	}
	return object, nil
}

func looksLikeMarkdownChecklist(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "\n") {
		return true
	}
	for _, prefix := range []string{"- [", "* [", "+ [", "[ ]", "[x]", "[X]", "[>]", "[~]", "[/]"} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func stringifyScalar(v any) (string, bool) {
	if v == nil {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val), true
	case json.Number:
		return strings.TrimSpace(val.String()), true
	case float64, int, int64, uint64:
		return strings.TrimSpace(fmt.Sprintf("%v", val)), true
	default:
		return "", false
	}
}

func normalizeTodoTaskArray(list []any) []any {
	normalized := make([]any, 0, len(list))
	for i, elem := range list {
		switch item := elem.(type) {
		case string:
			parsed := domaintool.ParseMarkdownTodoLines(item, "task-")
			for _, p := range parsed {
				normalized = append(normalized, map[string]any{
					"id":     p.ID,
					"task":   p.Task,
					"status": p.Status,
				})
			}
		case map[string]any:
			id, desc, rawStatus := domaintool.ExtractTodoFields(item)
			if id == "" {
				id = fmt.Sprintf("task-%d", i+1)
			}
			status := rawStatus
			if norm, ok := domaintool.NormalizeTodoStatus(rawStatus); ok {
				status = norm
			}
			normalized = append(normalized, map[string]any{
				"id":     id,
				"task":   desc,
				"status": status,
			})
		default:
			normalized = append(normalized, elem)
		}
	}
	return normalized
}

// normalizeTodoArguments canonicalizes common LLM variations for todo calls
// (action/status synonyms, numeric IDs, alias keys, empty optional fields,
// stringified checklists, and loosely typed task items) before JSON Schema
// validation so minor formatting quirks do not abort execution.
func normalizeTodoArguments(value any) any {
	if arr, ok := value.([]any); ok {
		return map[string]any{"tasks": normalizeTodoTaskArray(arr)}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}

	// Drop null values and empty optional strings that strict-output models
	// frequently populate alongside the active fields.
	for k, v := range object {
		if v == nil {
			delete(object, k)
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			delete(object, k)
		}
	}

	// Normalize action aliases.
	if _, exists := object["action"]; !exists {
		for _, alias := range []string{"op", "operation", "command", "type"} {
			if v, ok := object[alias]; ok {
				object["action"] = v
				delete(object, alias)
				break
			}
		}
	}

	// Normalize task_id aliases and coerce numeric IDs to strings.
	if _, exists := object["task_id"]; !exists {
		for _, alias := range []string{"id", "taskId", "number", "index", "key"} {
			if v, ok := object[alias]; ok {
				object["task_id"] = v
				delete(object, alias)
				break
			}
		}
	}
	if v, exists := object["task_id"]; exists {
		if s, ok := stringifyScalar(v); ok && s != "" {
			object["task_id"] = s
		} else if s == "" {
			delete(object, "task_id")
		}
	}

	// Normalize list aliases and string-valued plan fields to tasks/markdown.
	for _, alias := range []string{"todos", "items", "plan", "data", "list", "todo_list", "actions", "md"} {
		v, exists := object[alias]
		if !exists {
			continue
		}
		switch typed := v.(type) {
		case []any:
			if _, hasTasks := object["tasks"]; !hasTasks {
				object["tasks"] = typed
			}
			delete(object, alias)
		case string:
			if _, hasMD := object["markdown"]; !hasMD && strings.TrimSpace(typed) != "" {
				object["markdown"] = typed
			}
			delete(object, alias)
		}
	}
	if rawTasks, exists := object["tasks"]; exists {
		if s, ok := rawTasks.(string); ok {
			if _, hasMD := object["markdown"]; !hasMD && strings.TrimSpace(s) != "" {
				object["markdown"] = s
			}
			delete(object, "tasks")
		}
	}
	if rawContent, exists := object["content"]; exists {
		switch typed := rawContent.(type) {
		case []any:
			if _, hasTasks := object["tasks"]; !hasTasks {
				object["tasks"] = typed
			}
			delete(object, "content")
		case string:
			if _, hasMD := object["markdown"]; !hasMD && looksLikeMarkdownChecklist(typed) {
				object["markdown"] = typed
				delete(object, "content")
			} else if _, hasTask := object["task"]; !hasTask {
				object["task"] = strings.TrimSpace(typed)
				delete(object, "content")
			}
		}
	}

	// Normalize task description aliases.
	if _, exists := object["task"]; !exists {
		for _, alias := range []string{"description", "title", "text", "desc", "summary", "item", "body", "label"} {
			if s, ok := object[alias].(string); ok && strings.TrimSpace(s) != "" {
				object["task"] = strings.TrimSpace(s)
				delete(object, alias)
				break
			}
		}
	}

	// Normalize status aliases, booleans, and synonym values.
	if _, exists := object["status"]; !exists {
		if v, ok := object["state"]; ok {
			object["status"] = v
			delete(object, "state")
		} else if done, ok := object["done"].(bool); ok {
			if done {
				object["status"] = "completed"
			} else {
				object["status"] = "pending"
			}
			delete(object, "done")
		} else if completed, ok := object["completed"].(bool); ok {
			if completed {
				object["status"] = "completed"
			} else {
				object["status"] = "pending"
			}
			delete(object, "completed")
		} else if inProgress, ok := object["in_progress"].(bool); ok {
			if inProgress {
				object["status"] = "in_progress"
			} else {
				object["status"] = "pending"
			}
			delete(object, "in_progress")
		}
	}
	if rawStatus, ok := object["status"].(string); ok {
		if norm, valid := domaintool.NormalizeTodoStatus(rawStatus); valid {
			object["status"] = norm
		}
	}

	// Drop empty tasks array when markdown or a non-clearing action is present.
	if rawTasks, ok := object["tasks"].([]any); ok {
		_, hasMD := object["markdown"]
		_, hasAction := object["action"]
		if len(rawTasks) == 0 && (hasMD || hasAction) {
			delete(object, "tasks")
		} else {
			object["tasks"] = normalizeTodoTaskArray(rawTasks)
		}
	}

	// Normalize action verbs into the canonical schema enum.
	_, hasMarkdown := object["markdown"]
	_, hasTasks := object["tasks"]
	_, hasTaskID := object["task_id"]
	if rawAction, ok := object["action"].(string); ok {
		act := strings.ToLower(strings.TrimSpace(rawAction))
		if hasMarkdown || hasTasks {
			switch act {
			case "update", "replace", "set", "create", "init", "write", "save", "sync", "plan", "modify", "edit", "upsert", "overwrite", "put":
				if !hasTaskID {
					delete(object, "action")
				} else {
					object["action"] = "update"
				}
			case "add", "append", "insert", "push", "new":
				object["action"] = "add"
			case "clear", "empty", "wipe", "delete_all", "remove_all":
				delete(object, "action")
				delete(object, "markdown")
				object["tasks"] = []any{}
			}
		} else {
			switch act {
			case "complete", "completed", "finish", "finished", "done", "check":
				object["action"] = "update"
				if _, hasStatus := object["status"]; !hasStatus {
					object["status"] = "completed"
				}
			case "start", "begin", "in_progress", "in-progress", "inprogress", "active", "working":
				object["action"] = "update"
				if _, hasStatus := object["status"]; !hasStatus {
					object["status"] = "in_progress"
				}
			case "pending", "reopen", "uncheck", "todo":
				object["action"] = "update"
				if _, hasStatus := object["status"]; !hasStatus {
					object["status"] = "pending"
				}
			case "reset":
				if hasTaskID {
					object["action"] = "update"
					if _, hasStatus := object["status"]; !hasStatus {
						object["status"] = "pending"
					}
				} else {
					delete(object, "action")
					object["tasks"] = []any{}
				}
			case "clear", "empty", "wipe", "delete_all", "remove_all":
				delete(object, "action")
				object["tasks"] = []any{}
			case "delete", "del", "rm", "drop":
				object["action"] = "remove"
			case "create", "insert", "append", "new", "push":
				object["action"] = "add"
			case "modify", "edit", "change", "set", "mark", "set_status", "upsert":
				object["action"] = "update"
			case "list", "get", "show", "view", "read", "status", "inspect":
				object["action"] = "list"
			case "update", "add", "remove":
				object["action"] = act
			}
		}
	}

	return object
}

// CanonicalArguments normalizes model arguments and guard fingerprints. Direct
// tool callers still receive strict schema validation through Prepare.
func CanonicalArguments(name, arguments string) string {
	if len(arguments) > 8<<20 {
		return arguments
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(arguments))
	if err != nil {
		repaired := domaintool.RepairJSON(arguments)
		value, err = jsonschema.UnmarshalJSON(strings.NewReader(repaired))
		if err != nil {
			return arguments
		}
		arguments = repaired
	}
	if normalized, normalizeErr := normalizeToolArguments(name, arguments, value); normalizeErr == nil {
		value = normalized
	}
	// Keep invalid requests invalid while giving their JSON a stable identity.
	// Prepare remains responsible for rejecting conflicting or missing fields.
	encoded, err := json.Marshal(value)
	if err != nil {
		return arguments
	}
	return string(encoded)
}
