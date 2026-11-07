package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/resources"
)

func (a *Agent) emitOnceMemoryError(msg string) {
	if a == nil || msg == "" {
		return
	}
	a.stateMu.Lock()
	if a.emittedMemoryErrors == nil {
		a.emittedMemoryErrors = make(map[string]bool)
	}
	if a.emittedMemoryErrors[msg] {
		a.stateMu.Unlock()
		return
	}
	a.emittedMemoryErrors[msg] = true
	a.stateMu.Unlock()
	a.emit(Event{Type: "integration_error", Output: msg, IsError: true})
}

func (a *Agent) LoadMemoryContext() string {
	if a == nil || (a.Config != nil && !a.Config.ContextFiles) {
		return ""
	}
	var output strings.Builder
	read := func(path, kind string) bool {
		content, exists, err := resources.ReadCachedTextFile(path, 256<<10)
		if !exists && err == nil {
			return false
		}
		if err != nil {
			a.emitOnceMemoryError(path + ": " + err.Error())
			return true
		}
		if text := strings.TrimSpace(content); text != "" {
			fmt.Fprintf(&output, "\n\n%s (%s):\n%s", kind, path, text)
		}
		return true
	}

	agentDir := resources.AgentDirectory(a.ConfigPath)
	read(filepath.Join(agentDir, "LOOP.md"), "User directives")

	trusted := a.Config == nil || a.Config.ProjectResources
	if !trusted {
		read(filepath.Join(agentDir, "MEMORY.md"), "Project architecture and learnings")
		instructions, diagnostics := resources.InstructionsForDirectories(agentDir)
		output.WriteString(instructions)
		for _, diagnostic := range diagnostics {
			a.emitOnceMemoryError(diagnostic.Path + ": " + diagnostic.Message)
		}
		return output.String()
	}

	roots := resources.Ancestors(a.WorkspaceRoot)
	loadedMemory := false
	for i := len(roots) - 1; i >= 0; i-- {
		if read(filepath.Join(roots[i], "MEMORY.md"), "Project architecture and learnings") || read(filepath.Join(roots[i], ".loop", "MEMORY.md"), "Project architecture and learnings") {
			loadedMemory = true
			break
		}
	}
	if !loadedMemory {
		read(filepath.Join(agentDir, "MEMORY.md"), "Project architecture and learnings")
	}

	dirs := make([]string, 0, 1+len(roots))
	dirs = append(dirs, agentDir)
	dirs = append(dirs, roots...)
	instructions, diagnostics := resources.InstructionsForDirectories(dirs...)
	output.WriteString(instructions)
	for _, diagnostic := range diagnostics {
		a.emitOnceMemoryError(diagnostic.Path + ": " + diagnostic.Message)
	}
	return output.String()
}
