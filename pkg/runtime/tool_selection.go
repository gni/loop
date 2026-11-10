package runtime

import (
	"fmt"
	"loop/pkg/agent/tool"
)

func (s *Session) validateTools(names []string) error {
	for _, name := range names {
		if !tool.IsSafeIdentifier(name) {
			return fmt.Errorf("invalid tool name %q", name)
		}
		if _, exists := s.Agent.Registry.Lookup(name); exists {
			continue
		}
		if owner, found := s.Agent.MCPToolServer(name); found {
			if server, exists := s.Agent.Config.MCPServers[owner]; exists && server.Enabled {
				continue
			}
		}
		return fmt.Errorf("unknown tool %q", name)
	}
	return nil
}
