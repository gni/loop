package resources

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/storage"
)

type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type cachedAncestors struct {
	checkedAt time.Time
	dirs      []string
}

type cachedTextFile struct {
	modTime time.Time
	size    int64
	content string
}

var (
	instructionsCacheMu sync.Mutex
	ancestorsCache      = make(map[string]cachedAncestors)
	fileContentCache    = make(map[string]cachedTextFile)
)

func ReadCachedTextFile(path string, maxBytes int64) (string, bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		instructionsCacheMu.Lock()
		delete(fileContentCache, path)
		instructionsCacheMu.Unlock()
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Size() > maxBytes {
		return "", true, fmt.Errorf("file exceeds %d KiB", maxBytes>>10)
	}
	instructionsCacheMu.Lock()
	if cached, ok := fileContentCache[path]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		instructionsCacheMu.Unlock()
		return cached.content, true, nil
	}
	instructionsCacheMu.Unlock()

	data, err := storage.ReadFile(path, maxBytes)
	if err != nil {
		return "", true, err
	}
	text := string(data)
	instructionsCacheMu.Lock()
	fileContentCache[path] = cachedTextFile{modTime: info.ModTime(), size: info.Size(), content: text}
	instructionsCacheMu.Unlock()
	return text, true, nil
}

func Ancestors(workspace string) []string {
	clean := filepath.Clean(workspace)
	now := time.Now()
	instructionsCacheMu.Lock()
	if cached, ok := ancestorsCache[clean]; ok && now.Sub(cached.checkedAt) < time.Second {
		out := append([]string(nil), cached.dirs...)
		instructionsCacheMu.Unlock()
		return out
	}
	instructionsCacheMu.Unlock()

	var directories []string
	for directory := clean; ; directory = filepath.Dir(directory) {
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

	instructionsCacheMu.Lock()
	ancestorsCache[clean] = cachedAncestors{checkedAt: now, dirs: append([]string(nil), directories...)}
	instructionsCacheMu.Unlock()
	return directories
}

func Instructions(agentDir, workspace string) (string, []Diagnostic) {
	return InstructionsForDirectories(append([]string{agentDir}, Ancestors(workspace)...)...)
}

func InstructionsForDirectories(directories ...string) (string, []Diagnostic) {
	var output strings.Builder
	var diagnostics []Diagnostic
	seen := make(map[string]bool, len(directories))
	for _, dir := range directories {
		if dir == "" {
			continue
		}
		cleanDir := filepath.Clean(dir)
		if seen[cleanDir] {
			continue
		}
		seen[cleanDir] = true

		candidateFiles := []string{
			"AGENTS.override.md",
			"AGENTS.md",
			"CLAUDE.md",
			".cursorrules",
			".windsurfrules",
			filepath.Join(".github", "copilot-instructions.md"),
			"CONVENTIONS.md",
		}

		for _, name := range candidateFiles {
			path := filepath.Join(cleanDir, name)
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
			text, _, err := ReadCachedTextFile(path, 256<<10)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{path, err.Error()})
				break
			}
			fmt.Fprintf(&output, "\n\nInstructions (%s):\n%s", path, strings.TrimSpace(text))
			break
		}

		cursorRulesDir := filepath.Join(cleanDir, ".cursor", "rules")
		entries, err := os.ReadDir(cursorRulesDir)
		if err != nil {
			if !os.IsNotExist(err) {
				diagnostics = append(diagnostics, Diagnostic{cursorRulesDir, err.Error()})
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".mdc")) {
				continue
			}
			rulePath := filepath.Join(cursorRulesDir, entry.Name())
			text, exists, err := ReadCachedTextFile(rulePath, 128<<10)
			if err != nil {
				diagnostics = append(diagnostics, Diagnostic{rulePath, err.Error()})
				continue
			}
			if exists && strings.TrimSpace(text) != "" {
				fmt.Fprintf(&output, "\n\nInstructions (%s):\n%s", rulePath, strings.TrimSpace(text))
			}
		}
	}
	return output.String(), diagnostics
}
