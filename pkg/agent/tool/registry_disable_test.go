package tool

import (
	"strings"
	"testing"
)

type dummyTool struct {
	name string
}

func (d *dummyTool) Name() string { return d.name }
func (d *dummyTool) Definition() Tool {
	return Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        d.name,
			Description: "dummy tool " + d.name,
		},
	}
}
func (d *dummyTool) Execute(ctx AgentContext, args string) (string, error) {
	return "executed " + d.name, nil
}

func TestToolRegistryDisableAndEnable(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&dummyTool{name: "bash"})
	reg.Register(&dummyTool{name: "read"})
	reg.Register(&dummyTool{name: "write"})

	if reg.IsDisabled("bash") {
		t.Fatalf("expected bash to be enabled initially")
	}

	// Disable bash
	reg.Disable("bash")
	if !reg.IsDisabled("bash") {
		t.Fatalf("expected bash to be disabled")
	}
	// Normalization check: "run_command" normalizes to "bash"
	if !reg.IsDisabled("run_command") {
		t.Fatalf("expected run_command alias to also be reported as disabled")
	}

	// Available tools should not include bash
	avail := reg.GetAvailableTools(nil)
	if len(avail) != 2 {
		t.Fatalf("expected 2 available tools, got %d", len(avail))
	}
	for _, tool := range avail {
		if tool.Function.Name == "bash" {
			t.Fatalf("disabled tool bash should not be in available tools")
		}
	}

	// Execute should reject disabled tool
	_, err := reg.Execute(nil, "bash", "{}")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected error indicating tool is disabled, got %v", err)
	}

	// Re-enable bash
	reg.Enable("bash")
	if reg.IsDisabled("bash") {
		t.Fatalf("expected bash to be enabled after Enable")
	}

	// Now available tools should include bash
	avail = reg.GetAvailableTools(nil)
	if len(avail) != 3 {
		t.Fatalf("expected 3 available tools, got %d", len(avail))
	}

	// Execute should succeed
	out, err := reg.Execute(nil, "bash", "{}")
	if err != nil || out != "executed bash" {
		t.Fatalf("expected successful execution, got out=%q, err=%v", out, err)
	}
}

func TestToolRegistryDisableMultipleAndEnableAll(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&dummyTool{name: "bash"})
	reg.Register(&dummyTool{name: "read"})
	reg.Register(&dummyTool{name: "write"})

	reg.Disable("read", "write")
	if !reg.IsDisabled("read") || !reg.IsDisabled("write") {
		t.Fatalf("expected read and write to be disabled")
	}
	disabled := reg.GetDisabledTools()
	if len(disabled) != 2 {
		t.Fatalf("expected 2 disabled tools, got %d", len(disabled))
	}

	// Available tools should only have bash
	avail := reg.GetAvailableTools(nil)
	if len(avail) != 1 || avail[0].Function.Name != "bash" {
		t.Fatalf("expected only bash to be available, got %+v", avail)
	}

	// Enable all
	reg.EnableAll()
	if reg.IsDisabled("read") || reg.IsDisabled("write") {
		t.Fatalf("expected all tools to be enabled after EnableAll")
	}
	if len(reg.GetDisabledTools()) != 0 {
		t.Fatalf("expected 0 disabled tools, got %v", reg.GetDisabledTools())
	}
	avail = reg.GetAvailableTools(nil)
	if len(avail) != 3 {
		t.Fatalf("expected all 3 tools available, got %d", len(avail))
	}
}

func TestToolRegistryWithAllowlistAndDisabled(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&dummyTool{name: "bash"})
	reg.Register(&dummyTool{name: "read"})
	reg.Register(&dummyTool{name: "write"})

	// Allowlist contains bash and read, but bash is disabled
	reg.Disable("bash")
	avail := reg.GetAvailableTools([]string{"bash", "read"})
	if len(avail) != 1 || avail[0].Function.Name != "read" {
		t.Fatalf("expected only read to be available when bash is disabled, got %+v", avail)
	}
}
