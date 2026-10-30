package agent

import (
	"fmt"
	"loop/pkg/resources"
	"loop/pkg/storage"
	"os"
	"path/filepath"
	"strings"
)

func (a *Agent) LoadMemoryContext() string {
	if a.Config != nil && a.Config.NoContextFiles {
		return ""
	}
	var output strings.Builder
	read := func(path, kind string) bool {
		data, err := storage.ReadFile(path, 256<<10)
		if os.IsNotExist(err) {
			return false
		}
		if err != nil {
			a.emit(Event{Type: "integration_error", Output: err.Error(), IsError: true})
			return true
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			fmt.Fprintf(&output, "\n\n%s (%s):\n%s", kind, path, text)
		}
		return true
	}
	read(filepath.Join(resources.AgentDirectory(a.ConfigPath), "LOOP.md"), "User directives")
	roots := resources.Ancestors(a.WorkspaceRoot)
	for i := len(roots) - 1; i >= 0; i-- {
		if read(filepath.Join(roots[i], "MEMORY.md"), "Project architecture and learnings") || read(filepath.Join(roots[i], ".loop", "MEMORY.md"), "Project architecture and learnings") {
			break
		}
	}
	instructions, diagnostics := resources.Instructions(resources.AgentDirectory(a.ConfigPath), a.WorkspaceRoot)
	output.WriteString(instructions)
	for _, diagnostic := range diagnostics {
		a.emit(Event{Type: "integration_error", Output: diagnostic.Path + ": " + diagnostic.Message, IsError: true})
	}
	return output.String()
}
