package agent

import (
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"strings"
)

// syncToolAvailability keeps explicit permissions separate from disabled-by-feature
// tools. Registry state is a derived view, never a source for DisabledTools.
func (a *Agent) syncToolAvailability() {
	if a == nil || a.Registry == nil {
		return
	}
	a.Registry.EnableAll()
	if a.Config == nil {
		return
	}
	a.Registry.Disable(a.Config.DisabledTools...)
	if a.Config.AskUserMode == "disabled" {
		a.Registry.Disable("ask_user")
	}
	a.enforceOptionalTools()
}

func containsDisabledTool(names []string, name string) bool {
	for _, current := range names {
		if tool.NormalizeName(current) == name {
			return true
		}
	}
	return false
}

func (a *Agent) DisableTools(names ...string) {
	if a == nil || a.Registry == nil {
		return
	}
	if a.Config == nil {
		a.Registry.Disable(names...)
		a.markSystemPromptDirty()
		return
	}
	for _, name := range names {
		normalized := tool.NormalizeName(name)
		if normalized == "" {
			continue
		}
		if normalized == "ask_user" {
			a.Config.SetAskUserMode("disabled")
			continue
		}
		if !containsDisabledTool(a.Config.DisabledTools, normalized) {
			a.Config.DisabledTools = append(a.Config.DisabledTools, normalized)
		}
	}
	a.syncToolAvailability()
	a.markSystemPromptDirty()
}

func (a *Agent) EnableTools(names ...string) {
	if a == nil || a.Registry == nil {
		return
	}
	if a.Config == nil {
		a.Registry.Enable(names...)
		a.markSystemPromptDirty()
		return
	}

	wasMCPDisabled := !a.Config.MCP
	wasSubagentsDisabled := a.Config.MaxSubagentDepth <= 0
	requested := make(map[string]bool, len(names))
	for _, name := range names {
		normalized := tool.NormalizeName(name)
		if normalized == "" {
			continue
		}
		requested[normalized] = true
		if normalized == "ask_user" && a.Config.AskUserMode == "disabled" {
			a.Config.SetAskUserMode("interactive")
		}
		if normalized == "load_mcp" || normalized == "search_tools" || normalized == "list_mcp_resources" || normalized == "list_mcp_resource_templates" || normalized == "read_mcp_resource" || strings.HasPrefix(normalized, "mcp__") {
			a.Config.MCP = true
		}
		if tool.IsSubagentTool(normalized) {
			if a.Config.MaxSubagentDepth <= 0 {
				a.Config.MaxSubagentDepth = 1
			}
		}
	}

	// Turning on a feature through a single tool must not silently enable all
	// related tools. Those other tools remain individually opt-in.
	if wasMCPDisabled && a.Config.MCP {
		for _, name := range []string{"load_mcp", "search_tools", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource"} {
			if !requested[name] && !containsDisabledTool(a.Config.DisabledTools, name) {
				a.Config.DisabledTools = append(a.Config.DisabledTools, name)
			}
		}
		a.enableMCPDeferred()
	}
	if wasSubagentsDisabled && a.Config.MaxSubagentDepth > 0 {
		for _, name := range []string{"create_subagent", "remove_subagent", "list_subagents", "inspect_subagent"} {
			if !requested[name] && !containsDisabledTool(a.Config.DisabledTools, name) {
				a.Config.DisabledTools = append(a.Config.DisabledTools, name)
			}
		}
	}

	filtered := a.Config.DisabledTools[:0]
	for _, name := range a.Config.DisabledTools {
		if !requested[tool.NormalizeName(name)] {
			filtered = append(filtered, name)
		}
	}
	a.Config.DisabledTools = filtered
	a.syncToolAvailability()
	a.markSystemPromptDirty()
}

// enableMCPDeferred makes configured servers discoverable when MCP is enabled
// during an existing session, without blocking the tools menu on connections.
func (a *Agent) enableMCPDeferred() {
	a.MCPClientsMu.Lock()
	if a.mcpPending == nil {
		a.mcpPending = make(map[string]config.MCPServerConfig)
	}
	for name, server := range a.Config.MCPServers {
		if server.Enabled && a.MCPClients[name] == nil {
			a.mcpPending[name] = server
		}
	}
	a.MCPClientsMu.Unlock()
	a.registerResourceTools()
}

func (a *Agent) EnableAllTools() {
	if a == nil || a.Registry == nil {
		return
	}
	if a.Config == nil {
		a.Registry.EnableAll()
		a.markSystemPromptDirty()
		return
	}
	wasMCPDisabled := !a.Config.MCP
	a.Config.DisabledTools = nil
	if a.Config.AskUserMode == "disabled" {
		a.Config.SetAskUserMode("interactive")
	}
	a.Config.MCP = true
	if wasMCPDisabled {
		a.enableMCPDeferred()
	}
	if a.Config.MaxSubagentDepth <= 0 {
		a.Config.MaxSubagentDepth = 1
	}
	a.syncToolAvailability()
	a.markSystemPromptDirty()
}

func (a *Agent) IsToolDisabled(name string) bool {
	if a == nil || a.Registry == nil {
		return false
	}
	return a.Registry.IsDisabled(name)
}

func (a *Agent) GetDisabledTools() []string {
	if a == nil || a.Registry == nil {
		return nil
	}
	return a.Registry.GetDisabledTools()
}

// enforceOptionalTools enforces opt-in even after enable-all and config reloads.
func (a *Agent) enforceOptionalTools() {
	if a == nil || a.Registry == nil || a.Config == nil {
		return
	}
	if !a.Config.MCP {
		a.Registry.Disable("load_mcp", "search_tools", "list_mcp_resources", "list_mcp_resource_templates", "read_mcp_resource")
		for name := range a.Registry.GetAllExecutors() {
			if strings.HasPrefix(name, "mcp__") {
				a.Registry.Disable(name)
			}
		}
	}
	for name := range a.Registry.GetAllExecutors() {
		if !a.Config.LocalPlugins && strings.HasPrefix(name, "plugin__") {
			a.Registry.Disable(name)
		}
		if strings.HasPrefix(name, "mcp__") {
			if owner, found := a.MCPToolServer(name); found {
				server, ok := a.Config.MCPServers[owner]
				if !ok || !server.Enabled {
					a.Registry.Disable(name)
				}
			}
		}
	}
	if a.Config.MaxSubagentDepth <= 0 {
		a.Registry.Disable("create_subagent", "remove_subagent", "list_subagents", "inspect_subagent")
		for name := range a.Registry.GetAllExecutors() {
			if strings.HasPrefix(name, "subagent__") {
				a.Registry.Disable(name)
			}
		}
	}
}
