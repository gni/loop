package resources

import (
	"fmt"
	"loop/pkg/storage"
	"os"
	"path/filepath"
	"strings"
)

type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func Ancestors(workspace string) []string {
	var directories []string
	for directory := filepath.Clean(workspace); ; directory = filepath.Dir(directory) {
		directories = append(directories, directory)
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			break
		}
		if filepath.Dir(directory) == directory {
			break
		}
	}
	for left, right := 0, len(directories)-1; left < right; left, right = left+1, right-1 {
		directories[left], directories[right] = directories[right], directories[left]
	}
	return directories
}

func Instructions(agentDir, workspace string) (string, []Diagnostic) {
	var output strings.Builder
	var diagnostics []Diagnostic
	directories := append([]string{agentDir}, Ancestors(workspace)...)
	for _, dir := range directories {
		for _, name := range []string{"AGENTS.override.md", "AGENTS.md", "CLAUDE.md"} {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{path, err.Error()})
				break
			}
			if info.Size() > 256<<10 {
				diagnostics = append(diagnostics, Diagnostic{path, "instruction file exceeds 256 KiB"})
				break
			}
			data, err := storage.ReadFile(path, 256<<10)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{path, err.Error()})
				break
			}
			fmt.Fprintf(&output, "\n\nInstructions (%s):\n%s", path, strings.TrimSpace(string(data)))
			break
		}
	}
	return output.String(), diagnostics
}
