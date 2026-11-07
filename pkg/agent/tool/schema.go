package tool

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"

	filetool "loop/pkg/agent/tool/file"
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
	if !json.Valid([]byte(arguments)) {
		return nil, "", fmt.Errorf("invalid JSON tool arguments for %q", name)
	}
	var value any
	if err := json.Unmarshal([]byte(arguments), &value); err != nil {
		return nil, "", fmt.Errorf("JSON validation failed: %w", err)
	}
	value = normalizeToolArguments(executor.Name(), arguments, value)
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
		return nil, "", fmt.Errorf("tool %q arguments: %w", name, err)
	}
	return executor, string(encoded), nil
}

func normalizeToolArguments(name, arguments string, value any) any {
	name = NormalizeName(name)
	object, ok := value.(map[string]any)
	if !ok {
		return value
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
	case "read", "write", "edit", "list_directory":
		if path := filetool.ExtractFilePath(arguments); path != "" {
			object["path"] = path
		}
		if name == "write" {
			if content, ok := filetool.ExtractFileContent(arguments); ok {
				object["content"] = content
			}
		}
	case "find_files":
		copyAlias("pattern", "glob")
		if path := filetool.ExtractFilePath(arguments); path != "" {
			object["path"] = path
		}
	case "bash":
		copyAlias("command", "cmd", "arguments")
		copyAlias("dir", "cwd")
	case "todo":
		copyAlias("tasks", "actions")
		delete(object, "actions")
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
	return object
}

// CanonicalArguments is used at the model adapter boundary. Direct tool callers
// still receive normalization and strict schema validation through Prepare.
func CanonicalArguments(name, arguments string) string {
	if len(arguments) > 8<<20 {
		return arguments
	}
	var value any
	if json.Unmarshal([]byte(arguments), &value) != nil {
		repaired := domaintool.RepairJSON(arguments)
		if json.Unmarshal([]byte(repaired), &value) != nil {
			return arguments
		}
		arguments = repaired
	}
	value = normalizeToolArguments(name, arguments, value)
	encoded, err := json.Marshal(value)
	if err != nil {
		return arguments
	}
	return string(encoded)
}
