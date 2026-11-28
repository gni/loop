package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"loop/pkg/domain/message"
	"loop/pkg/process"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Command struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func Commands(directories []string) []Command {
	seen := make(map[string]bool)
	var commands []Command
	for _, directory := range directories {
		evalDir, err := filepath.EvalSymlinks(directory)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) == ".json" {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				continue
			}
			rel, relErr := filepath.Rel(evalDir, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			info, err := os.Stat(resolved)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
				continue
			}
			base := strings.TrimSuffix(path, filepath.Ext(path))
			if _, err := os.Stat(path + ".json"); err == nil {
				continue
			}
			if _, err := os.Stat(base + ".json"); err == nil {
				continue
			}
			name := "/" + strings.ToLower(strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name())))
			if seen[name] {
				continue
			}
			seen[name] = true
			commands = append(commands, Command{Name: name, Path: path})
		}
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands
}

func RunCommand(ctx context.Context, command Command, args []string, workspace string, messages []message.Message, timeout time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	result, err := process.Run(ctx, process.Options{Executable: command.Path, Arguments: args, Directory: workspace, Input: bytes.NewReader(data), Timeout: timeout})
	if err != nil {
		if result.ExitCode != nil && *result.ExitCode == 0 {
			return result.Stdout, fmt.Errorf("extension completed with exit code 0, but capturing its output failed; effects may already be applied: %w", err)
		}
		if details := strings.TrimSpace(result.Stderr); details != "" {
			return result.Stdout, fmt.Errorf("extension failed: %w (details: %s)", err, details)
		}
		return result.Stdout, fmt.Errorf("extension failed: %w", err)
	}
	return result.Stdout, nil
}
