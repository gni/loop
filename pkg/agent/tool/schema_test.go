package tool

import (
	"encoding/json"
	"testing"
)

type constrainedTool struct {
	calls  int
	schema JSONSchema
}

func (t *constrainedTool) Name() string { return "constrained" }
func (t *constrainedTool) Definition() Tool {
	return Tool{Type: "function", Function: FunctionDefinition{Name: t.Name(), Parameters: t.schema}}
}
func (t *constrainedTool) Execute(ctx AgentContext, args string) (string, error) {
	t.calls++
	return args, nil
}

func TestSchemaConstraintsBlockExecution(t *testing.T) {
	executor := &constrainedTool{}
	schema := `{"type":"object","required":["count","mode"],"additionalProperties":false,"properties":{"count":{"type":"integer","minimum":1,"maximum":3},"mode":{"enum":["read","write"]},"label":{"type":["string","null"],"maxLength":4},"items":{"type":"array","items":{"type":"integer"},"maxItems":2}}}`
	if err := json.Unmarshal([]byte(schema), &executor.schema); err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	registry.Register(executor)
	for _, args := range []string{`{}`, `{"count":0,"mode":"read"}`, `{"count":2.5,"mode":"read"}`, `{"count":2,"mode":"other"}`, `{"count":2,"mode":"read","extra":true}`, `{"count":2,"mode":"read","items":[1,2,3]}`, `{"count":2,"mode":"read","label":"longer"}`} {
		if _, err := registry.Execute(nil, executor.Name(), args); err == nil {
			t.Fatalf("invalid arguments accepted: %s", args)
		}
	}
	if executor.calls != 0 {
		t.Fatal("invalid call reached executor")
	}
	if _, err := registry.Execute(nil, executor.Name(), `{"count":2,"mode":"read","label":null}`); err != nil {
		t.Fatal(err)
	}
	registry.Disable(executor.Name())
	registry.Unregister(executor.Name())
	registry.Register(executor)
	if _, err := registry.Execute(nil, executor.Name(), `{"count":2,"mode":"read"}`); err == nil {
		t.Fatal("registration bypassed disabled tool policy")
	}
}
