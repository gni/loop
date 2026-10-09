package tool

import (
	"testing"
)

func TestCompressToolDefinition(t *testing.T) {
	origTool := Tool{
		Type: "function",
		Function: FunctionDefinition{
			Name:        "bash",
			Description: "Very long description about running commands in bash and terminal",
			Parameters: JSONSchema{
				Type: "object",
				Properties: map[string]SchemaProp{
					"command": {Type: "string", Description: "Detailed description of command to run"},
				},
			},
		},
	}

	compressed := CompressToolDefinition(origTool)
	if compressed.Function.Description == origTool.Function.Description {
		t.Errorf("expected description to be compressed")
	}
	if compressed.Function.Parameters.Properties["command"].Description == origTool.Function.Parameters.Properties["command"].Description {
		t.Errorf("expected parameter description to be compressed")
	}

	// Test PrepareToolDefinitions with compact=false and compact=true
	tools := []Tool{origTool}
	uncompressed := PrepareToolDefinitions(tools, false)
	if uncompressed[0].Function.Description != origTool.Function.Description {
		t.Errorf("expected uncompressed tool to remain unchanged when compact=false")
	}

	prepared := PrepareToolDefinitions(tools, true)
	if prepared[0].Function.Description == origTool.Function.Description {
		t.Errorf("expected prepared tool to be compressed when compact=true")
	}
}
