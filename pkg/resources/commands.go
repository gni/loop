package resources

import (
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
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) == ".json" {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Stat(path)
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
	data, err := json.Marshal(messages)
	if err != nil {
		return "", err
	}
	result, err := process.Run(ctx, process.Options{Executable: command.Path, Arguments: args, Directory: workspace, Input: strings.NewReader(string(data)), Timeout: timeout})
	if err != nil {
		return result.Stdout, fmt.Errorf("extension failed: %w (details: %s)", err, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}
