package agent

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadMemoryContext loads global (~/.loop/LOOP.md) and project (MEMORY.md) memory context.
func (a *Agent) LoadMemoryContext() string {
	var sb strings.Builder

	// 1. Global Memory (~/.loop/LOOP.md)
	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, ".loop", "LOOP.md")
		if data, err := os.ReadFile(globalPath); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if len(trimmed) > 0 {
				sb.WriteString(RuntimeMessagef("user_directives_header", "\n\nUser Directives (%s):\nAdhere to these global preferences and personal mandates strictly:\n%s", globalPath, trimmed))
			}
		}
	}

	// 2. Project Memory (current workspace MEMORY.md)
	// We search in current directory or traverse up to git root or stop at workspace root
	wd, err := os.Getwd()
	if err == nil {
		dir := wd
		for {
			found := false
			for _, p := range []string{filepath.Join(dir, "MEMORY.md"), filepath.Join(dir, ".loop", "MEMORY.md")} {
				if data, err := os.ReadFile(p); err == nil {
					if trimmed := strings.TrimSpace(string(data)); len(trimmed) > 0 {
						sb.WriteString(RuntimeMessagef("project_memory_header", "\n\nProject Architecture & Learnings (%s):\nFollow these repository conventions and architectural decisions strictly:\n%s", p, trimmed))
					}
					found = true
					break
				}
			}
			if found {
				break
			}

			// Stop if we reach git root, workspace root, or root directory
			if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
				break
			}
			if dir == a.WorkspaceRoot {
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	return sb.String()
}
