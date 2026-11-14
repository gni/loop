package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"loop/pkg/agent/tool"
	"loop/pkg/resources"
)

func (s *Session) Commands() []resources.Command {
	cfg := s.Agent.Config
	if !cfg.LocalPlugins {
		return nil
	}
	directories := append(tool.ExtensionSearchDirs(s.Workspace), cfg.ExtensionDirectories...)
	var allowed []resources.Command
	for _, command := range resources.Commands(directories) {
		if s.Agent.ProjectResourceAllowed(command.Path) {
			allowed = append(allowed, command)
		}
	}
	return allowed
}

func (s *Session) ExecuteCommand(ctx context.Context, name string, args []string) (string, error) {
	ctx, err := s.reserve(ctx)
	if err != nil {
		return "", err
	}
	defer s.releaseRun()
	if lifetime := s.Agent.Lifetime; lifetime != nil {
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			cancel()
		}
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	timeout := 15 * time.Second
	if s.Agent.Config.ExtensionTimeout > 0 {
		timeout = time.Duration(s.Agent.Config.ExtensionTimeout) * time.Second
	}
	for _, command := range s.Commands() {
		if command.Name == name {
			return resources.RunCommand(ctx, command, args, s.Workspace, s.MessagesSnapshot(), timeout)
		}
	}
	return "", fmt.Errorf("extension command %q is not available", name)
}
