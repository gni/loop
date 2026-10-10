package tool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	domaintool "loop/pkg/domain/tool"
)

type SchemaProp = domaintool.SchemaProp
type JSONSchema = domaintool.JSONSchema
type FunctionDefinition = domaintool.FunctionDefinition
type Tool = domaintool.Tool
type Skill = domaintool.Skill
type AgentContext = domaintool.AgentContext
type LiveOutputWriter = domaintool.LiveOutputWriter
type ToolExecutor = domaintool.ToolExecutor
type PromptContributor = domaintool.PromptContributor

var CompressToolDefinition = domaintool.CompressToolDefinition
var PrepareToolDefinitions = domaintool.PrepareToolDefinitions
var ParseToolArguments = domaintool.ParseToolArguments
var FirstNonEmpty = domaintool.FirstNonEmpty
var NewFunctionTool = domaintool.NewFunctionTool
var IsSafeIdentifier = domaintool.IsSafeIdentifier
var FormatToolSnippet = domaintool.FormatToolSnippet
var FormatToolGuidelines = domaintool.FormatToolGuidelines
var FormatToolDescription = domaintool.FormatToolDescription
var FormatParamDescription = domaintool.FormatParamDescription
var MasterToolPrompts = domaintool.MasterToolPrompts
var GetToolPrompt = domaintool.GetToolPrompt
var SetToolPrompt = domaintool.SetToolPrompt

// GetPromptSnippet retrieves the prompt snippet for a tool executor.
func GetPromptSnippet(t ToolExecutor) string {
	return domaintool.GetPromptSnippet(t)
}

// GetPromptGuidelines retrieves the prompt guidelines for a tool executor.
func GetPromptGuidelines(t ToolExecutor) []string {
	return domaintool.GetPromptGuidelines(t)
}

type ToolRegistry struct {
	tools map[string]ToolExecutor
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]ToolExecutor)}
}

func (r *ToolRegistry) Register(t ToolExecutor) {
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Unregister(name string) {
	delete(r.tools, name)
}

func (r *ToolRegistry) UnregisterPrefix(prefix string) {
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
		}
	}
}

func (r *ToolRegistry) Execute(ctx AgentContext, name string, arguments string) (string, error) {
	canonicalName := NormalizeName(name)
	executor, exists := r.tools[canonicalName]
	if !exists {
		executor, exists = r.tools[name]
	}
	if !exists {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	var temp interface{}
	// 1. Try parsing the raw arguments first
	if err := json.Unmarshal([]byte(arguments), &temp); err == nil {
		return executor.Execute(ctx, arguments)
	}

	// 2. If it fails, try repairing
	repairedArgs := domaintool.RepairJSON(arguments)
	if err := json.Unmarshal([]byte(repairedArgs), &temp); err == nil {
		return executor.Execute(ctx, repairedArgs)
	} else {
		return "", fmt.Errorf("JSON validation failed: %w.\nParameters generated: %s\nRecommendation: Please output valid JSON. Double-check closing braces, ensure internal double-quotes are escaped, and verify that newlines inside strings are escaped as '\\n'.", err, arguments)
	}
}

func (r *ToolRegistry) GetAvailableTools(allowlist []string) []Tool {
	var allTools []Tool
	for name, t := range r.tools {
		if len(allowlist) == 0 {
			allTools = append(allTools, t.Definition())
		} else {
			allowed := false
			for _, allowedName := range allowlist {
				if name == allowedName || strings.HasPrefix(name, "mcp__") {
					allowed = true
					break
				}
			}
			if allowed {
				allTools = append(allTools, t.Definition())
			}
		}
	}

	sort.Slice(allTools, func(i, j int) bool {
		return allTools[i].Function.Name < allTools[j].Function.Name
	})

	return allTools
}

func (r *ToolRegistry) GetAllExecutors() map[string]ToolExecutor {
	res := make(map[string]ToolExecutor, len(r.tools))
	for k, v := range r.tools {
		res[k] = v
	}
	return res
}
