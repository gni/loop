package tool

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

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
var MasterAgentTemplates = domaintool.MasterAgentTemplates
var StringProp = domaintool.StringProp
var NumberProp = domaintool.NumberProp
var BoolProp = domaintool.BoolProp
var RuntimeMessage = domaintool.RuntimeMessage
var RuntimeMessagef = domaintool.RuntimeMessagef

// GetPromptSnippet retrieves the prompt snippet for a tool executor.
func GetPromptSnippet(t ToolExecutor) string {
	return domaintool.GetPromptSnippet(t)
}

// GetPromptGuidelines retrieves the prompt guidelines for a tool executor.
func GetPromptGuidelines(t ToolExecutor) []string {
	return domaintool.GetPromptGuidelines(t)
}

// ToolRegistry is safe for concurrent use: parallel tool workers execute from
// several goroutines, and MCP servers register and unregister their tools while a
// turn is in flight.
type ToolRegistry struct {
	mu       sync.RWMutex
	tools    map[string]ToolExecutor
	disabled map[string]bool

	// UnknownToolHook resolves a tool that is not registered yet. It exists for MCP
	// servers deferred until first use: their executors cannot be registered before the
	// server is connected, so a call to one of their tools has to trigger the connect.
	// The hook is invoked with no registry lock held, so it may register freely; it must
	// not call back into the registry while doing so.
	UnknownToolHook func(name string) (ToolExecutor, bool)
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:    make(map[string]ToolExecutor),
		disabled: make(map[string]bool),
	}
}

func (r *ToolRegistry) Register(t ToolExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.disabled, name)
	delete(r.disabled, NormalizeName(name))
}

func (r *ToolRegistry) UnregisterPrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.disabled, name)
		}
	}
}

// Disable marks one or more tools as disabled.
func (r *ToolRegistry) Disable(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled == nil {
		r.disabled = make(map[string]bool)
	}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		norm := NormalizeName(trimmed)
		r.disabled[norm] = true
		r.disabled[trimmed] = true
	}
}

// Enable re-enables one or more disabled tools.
func (r *ToolRegistry) Enable(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled == nil {
		return
	}
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		norm := NormalizeName(trimmed)
		delete(r.disabled, norm)
		delete(r.disabled, trimmed)
	}
}

// EnableAll clears all disabled tools, enabling every registered tool.
func (r *ToolRegistry) EnableAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disabled = make(map[string]bool)
}

// IsDisabled reports whether a tool has been disabled.
func (r *ToolRegistry) IsDisabled(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.disabled) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(name)
	return r.disabled[trimmed] || r.disabled[NormalizeName(trimmed)]
}

// GetDisabledTools returns a sorted list of unique disabled tool names.
func (r *ToolRegistry) GetDisabledTools() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var list []string
	seen := make(map[string]bool)
	for name := range r.disabled {
		norm := NormalizeName(name)
		if !seen[norm] {
			seen[norm] = true
			list = append(list, norm)
		}
	}
	sort.Strings(list)
	return list
}

// Lookup finds an executor by name, falling back to the normalized form.
func (r *ToolRegistry) Lookup(name string) (ToolExecutor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if executor, exists := r.tools[name]; exists {
		return executor, true
	}
	executor, exists := r.tools[NormalizeName(name)]
	return executor, exists
}

func (r *ToolRegistry) Execute(ctx AgentContext, name string, arguments string) (string, error) {
	if r.IsDisabled(name) {
		return "", fmt.Errorf("tool '%s' is disabled", name)
	}
	executor, exists := r.Lookup(name)
	if !exists && r.UnknownToolHook != nil {
		// Lookup has already released the read lock, so the hook can register.
		executor, exists = r.UnknownToolHook(name)
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	var allTools []Tool
	for name, t := range r.tools {
		norm := NormalizeName(name)
		if r.disabled != nil && (r.disabled[name] || r.disabled[norm]) {
			continue
		}
		if len(allowlist) == 0 {
			allTools = append(allTools, t.Definition())
		} else {
			allowed := false
			for _, allowedName := range allowlist {
				if name == allowedName || norm == NormalizeName(allowedName) || strings.HasPrefix(name, "mcp__") {
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
	r.mu.RLock()
	defer r.mu.RUnlock()

	res := make(map[string]ToolExecutor, len(r.tools))
	for k, v := range r.tools {
		res[k] = v
	}
	return res
}
