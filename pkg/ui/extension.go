package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loop/pkg/agent"
	"loop/pkg/db"
	"loop/pkg/domain/tool"
	"loop/pkg/ui/render"
)

type ExtensionInfo = render.ExtensionInfo

func init() {
	render.ExtensionCounter = func(workspaceRoot string) int {
		return len(ListExtensions(workspaceRoot))
	}
}

func ListExtensions(workspaceRoot string) []ExtensionInfo {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".loop", "extensions"))
	}
	if workspaceRoot != "" {
		dirs = append(dirs, filepath.Join(workspaceRoot, "extensions"))
	}

	var exts []ExtensionInfo
	seen := make(map[string]bool)

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		loc := "project"
		if strings.Contains(dir, ".loop") {
			loc = "global"
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if info, err := os.Stat(path); err == nil && info.Mode()&0111 != 0 {
				base := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
				cmdName := "/" + strings.ToLower(base)
				if !seen[cmdName] {
					seen[cmdName] = true
					exts = append(exts, ExtensionInfo{
						Name:     cmdName,
						Path:     path,
						Location: loc,
					})
				}
			}
		}
	}

	sort.Slice(exts, func(i, j int) bool { return exts[i].Name < exts[j].Name })
	return exts
}

func RunExtension(
	a *agent.Agent,
	cmdName string,
	args []string,
	messages *[]db.Message,
	w io.Writer,
) (bool, error) {
	if a.Config.DisableLocalPlugins {
		return true, fmt.Errorf("local extensions are disabled for this workspace (run with trust to enable)")
	}

	name := strings.TrimPrefix(cmdName, "/")
	if !tool.IsSafeIdentifier(name) {
		return false, nil
	}

	targetCmd := "/" + strings.ToLower(name)
	var extPath string
	for _, ext := range ListExtensions(a.GetWorkspaceRoot()) {
		if ext.Name == targetCmd {
			extPath = ext.Path
			break
		}
	}

	if extPath == "" {
		return false, nil
	}

	parentCtx := context.Background()
	if a != nil {
		parentCtx = a.Context()
	}
	ctxTimeout, cancel := context.WithTimeout(parentCtx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, extPath, args...)
	if messages != nil {
		if jsonData, err := json.Marshal(*messages); err == nil {
			cmd.Stdin = bytes.NewReader(jsonData)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctxTimeout.Err() == context.DeadlineExceeded {
		return true, fmt.Errorf("extension timed out after 15 seconds")
	}

	if stdout.Len() > 0 {
		_, _ = io.Copy(w, &stdout)
	}

	if err != nil {
		return true, fmt.Errorf("%w (details: %s)", err, strings.TrimSpace(stderr.String()))
	}

	return true, nil
}