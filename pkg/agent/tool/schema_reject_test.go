package tool

import (
	"strings"
	"testing"
)

type noOpTool struct{}

func (noOpTool) Name() string                                     { return "safe_test_tool" }
func (noOpTool) Definition() Tool                                 { return NewFunctionTool("safe_test_tool", "test", nil) }
func (noOpTool) Execute(_ AgentContext, _ string) (string, error) { return "ok", nil }

func TestMalformedNativeToolArgumentsAreRejected(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(noOpTool{})
	for _, invalid := range []string{`{"oops":`, `{"command":"abc",}`, `not JSON`, ``} {
		if _, _, err := registry.Prepare("safe_test_tool", invalid); err == nil || !strings.Contains(err.Error(), "invalid JSON") {
			t.Errorf("malformed arguments %q were not rejected: %v", invalid, err)
		}
	}
}
