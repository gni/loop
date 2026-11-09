package agent

import transporthttp "loop/pkg/transport/http"

// MCPServerStatus is a presentation-independent snapshot of a connection.
type MCPServerStatus struct {
	State string
	Tools int
	Error string
}

// MCPStartupErrors returns an isolated, redacted snapshot for startup warnings.
func (a *Agent) MCPStartupErrors() map[string]error {
	errors := make(map[string]error)
	if a == nil {
		return errors
	}
	a.MCPClientsMu.Lock()
	defer a.MCPClientsMu.Unlock()
	for name, err := range a.MCPStartErrors {
		if err != nil {
			errors[name] = transporthttp.RedactError(err)
		}
	}
	return errors
}

func (a *Agent) MCPServerStatuses() map[string]MCPServerStatus {
	statuses := make(map[string]MCPServerStatus)
	if a == nil {
		return statuses
	}
	a.MCPClientsMu.Lock()
	defer a.MCPClientsMu.Unlock()
	for name := range a.mcpPending {
		statuses[name] = MCPServerStatus{State: "deferred"}
	}
	for name, failure := range a.MCPStartErrors {
		if failure != nil {
			statuses[name] = MCPServerStatus{State: "failed", Error: transporthttp.RedactDiagnostic(failure.Error())}
		}
	}
	for name := range a.mcpConnecting {
		statuses[name] = MCPServerStatus{State: "connecting"}
	}
	for name, client := range a.MCPClients {
		if client == nil {
			continue
		}
		status := statuses[name]
		if client.initialized && client.healthy() {
			status.State, status.Error = "connected", ""
		} else if status.State != "failed" && status.State != "connecting" {
			status.State = "disconnected"
		}
		statuses[name] = status
	}
	if a.Registry != nil {
		for _, executor := range a.Registry.GetAllExecutors() {
			if mcp, ok := executor.(*mcpToolExecutor); ok {
				status := statuses[mcp.server]
				status.Tools++
				statuses[mcp.server] = status
			}
		}
	}
	return statuses
}
