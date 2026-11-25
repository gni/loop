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
	case "todo":
		if _, canonical := object["tasks"]; !canonical {
			copyAlias("tasks", "actions")
			delete(object, "actions")
		}
		_, hasMarkdown := object["markdown"]
		_, hasTasks := object["tasks"]
		_, hasTaskID := object["task_id"]
		_, hasID := object["id"]
		if (hasMarkdown || hasTasks) && !hasTaskID && !hasID {
			if action, ok := object["action"].(string); ok {
				switch strings.ToLower(strings.TrimSpace(action)) {
				case "update", "replace", "set", "create", "init", "write", "save", "sync", "plan", "modify", "edit":
					delete(object, "action")
				}
			}
		}
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
