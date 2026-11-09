package agent

import (
	"fmt"

	"loop/pkg/config"
)

// ReconnectMCPServers explicitly refreshes every enabled server, including
// deferred servers. AlwaysLoad is changed only in the connection snapshot.
// Callers must keep inference idle, as they do for ApplyConfig.
func (a *Agent) ReconnectMCPServers() error {
	if a == nil || a.Config == nil || a.Registry == nil {
		return fmt.Errorf("MCP configuration is unavailable")
	}
	if !a.Config.MCP {
		return fmt.Errorf("enable MCP before reconnecting")
	}
	servers := config.CopyMCPServers(a.Config.MCPServers)
	enabled := 0
	for name, server := range servers {
		if server.Enabled {
			enabled++
			server.AlwaysLoad = true
			servers[name] = server
		}
	}
	if enabled == 0 {
		return fmt.Errorf("enable at least one MCP server before reconnecting")
	}
	a.StopMCPServers()
	err := a.StartMCPServers(servers)
	a.syncToolAvailability()
	a.markSystemPromptDirty()
	return err
}
