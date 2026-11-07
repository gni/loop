package runtime

import (
	"fmt"
	"loop/pkg/agent/tool"
	"strings"
)

func (s *Session) validateTools(names []string) error {
	for _, name := range names {
		if !tool.IsSafeIdentifier(name) {
			return fmt.Errorf("invalid tool name %q", name)
		}
		if _, exists := s.Agent.Registry.Lookup(name); exists {
			continue
		}
		parts := strings.SplitN(name, "__", 3)
		if len(parts) == 3 && parts[0] == "mcp" {
			if server, exists := s.Agent.Config.MCPServers[parts[1]]; exists && server.Enabled {
				continue
			}
		}
		return fmt.Errorf("unknown tool %q", name)
	}
	return nil
}
