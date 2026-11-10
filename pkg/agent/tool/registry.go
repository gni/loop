package tool

import (
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
type BackgroundTaskOptions = domaintool.BackgroundTaskOptions
type LiveOutputWriter = domaintool.LiveOutputWriter
type ToolExecutor = domaintool.ToolExecutor

var PrepareToolDefinitions = domaintool.PrepareToolDefinitions
var ParseToolArguments = domaintool.ParseToolArguments
var FirstNonEmpty = domaintool.FirstNonEmpty
var NewFunctionTool = domaintool.NewFunctionTool
var IsSafeIdentifier = domaintool.IsSafeIdentifier
var FormatToolSnippet = domaintool.FormatToolSnippet
var FormatToolGuidelines = domaintool.FormatToolGuidelines
var FormatToolDescription = domaintool.FormatToolDescription
var FormatParamDescription = domaintool.FormatParamDescription

var MasterAgentTemplates = domaintool.MasterAgentTemplates
var AgentTemplatesSnapshot = domaintool.AgentTemplatesSnapshot
var StringProp = domaintool.StringProp
var NumberProp = domaintool.NumberProp

var RuntimeMessage = domaintool.RuntimeMessage
var RuntimeMessagef = domaintool.RuntimeMessagef
var DefaultAskUserSelection = domaintool.DefaultAskUserSelection

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
	hidden   map[string]bool
	schemas  map[string]compiledSchema
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
	delete(r.schemas, t.Name())
}

func (r *ToolRegistry) Unregister(name string) {
	name = NormalizeName(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tools, name)
	delete(r.schemas, name)
	delete(r.hidden, name)
}

func (r *ToolRegistry) UnregisterPrefix(prefix string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.schemas, name)
			delete(r.hidden, name)
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

// IsAllowedTool checks a tool against an explicit allowlist. Nil means unrestricted;
// an empty, non-nil list permits no tools.
func IsAllowedTool(name string, allowlist []string) bool {
	if allowlist == nil {
		return true
	}
	normalized := NormalizeName(name)
	for _, allowed := range allowlist {
		if allowed == name || NormalizeName(allowed) == normalized {
			return true
		}
	}
	return false
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
	executor, prepared, err := r.Prepare(name, arguments)
	if err != nil {
		return "", err
	}
	return executor.Execute(ctx, prepared)
}

// GetAvailableExecutors is the common visibility filter for API schemas and
// system-prompt tool guidance. Hidden, disabled, and disallowed tools stay out
// of both surfaces.
func (r *ToolRegistry) GetAvailableExecutors(allowlist []string) []ToolExecutor {
	return r.filteredExecutors(allowlist, false)
}

// GetSearchableExecutors includes tools awaiting explicit discovery. Disabled
// tools and tools outside the caller's allowlist remain excluded.
func (r *ToolRegistry) GetSearchableExecutors(allowlist []string) []ToolExecutor {
	return r.filteredExecutors(allowlist, true)
}

func (r *ToolRegistry) filteredExecutors(allowlist []string, includeHidden bool) []ToolExecutor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]ToolExecutor, 0, len(r.tools))
	for name, executor := range r.tools {
		norm := NormalizeName(name)
		if r.disabled[name] || r.disabled[norm] || (!includeHidden && (r.hidden[name] || r.hidden[norm])) || !IsAllowedTool(name, allowlist) {
			continue
		}
		all = append(all, executor)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name() < all[j].Name() })
	return all
}

func (r *ToolRegistry) GetAvailableTools(allowlist []string) []Tool {
	executors := r.GetAvailableExecutors(allowlist)
	definitions := make([]Tool, 0, len(executors))
	for _, executor := range executors {
		definitions = append(definitions, executor.Definition())
	}
	return definitions
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

func (r *ToolRegistry) Hide(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hidden == nil {
		r.hidden = make(map[string]bool)
	}
	r.hidden[NormalizeName(name)] = true
}
func (r *ToolRegistry) Expose(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hidden, NormalizeName(name))
}

func (r *ToolRegistry) ReplacePrefix(prefix string, executors map[string]ToolExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.tools {
		if strings.HasPrefix(name, prefix) {
			delete(r.tools, name)
			delete(r.schemas, name)
			delete(r.hidden, name)
		}
	}
	for name, executor := range executors {
		if strings.HasPrefix(name, prefix) {
			r.tools[name] = executor
		}
	}
}
