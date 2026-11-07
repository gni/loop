package ui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/resources"
)

type ExtensionInfo struct {
	Name     string
	Path     string
	Location string
}

const extensionListCacheTTL = time.Second

type cachedExtensionList struct {
	items   []ExtensionInfo
	fetched time.Time
}

var (
	extensionCacheMu sync.Mutex
	extensionCache   = make(map[string]cachedExtensionList)
)

func ListExtensions(workspaceRoot string, additional ...string) []ExtensionInfo {
	cacheKey := workspaceRoot + "\x00" + strings.Join(additional, "\x00")
	now := time.Now()

	extensionCacheMu.Lock()
	if cached, ok := extensionCache[cacheKey]; ok && now.Sub(cached.fetched) < extensionListCacheTTL {
		copied := make([]ExtensionInfo, len(cached.items))
		copy(copied, cached.items)
		extensionCacheMu.Unlock()
		return copied
	}
	extensionCacheMu.Unlock()

	directories := append(tool.ExtensionSearchDirs(workspaceRoot), additional...)
	var extensions []ExtensionInfo
	for _, command := range resources.Commands(directories) {
		location := "global"
		if workspaceRoot != "" {
			if relative, err := filepath.Rel(workspaceRoot, command.Path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				location = "project"
			}
		}
		extensions = append(extensions, ExtensionInfo{Name: command.Name, Path: command.Path, Location: location})
	}

	extensionCacheMu.Lock()
	if len(extensionCache) > 32 {
		clear(extensionCache)
	}
	copied := make([]ExtensionInfo, len(extensions))
	copy(copied, extensions)
	extensionCache[cacheKey] = cachedExtensionList{items: copied, fetched: now}
	extensionCacheMu.Unlock()

	return extensions
}

func ListAgentExtensions(a *agent.Agent) []ExtensionInfo {
	if a == nil {
		return ListExtensions("")
	}
	var additional []string
	if a.Config != nil {
		additional = a.Config.ExtensionDirectories
	}
	all := ListExtensions(a.GetWorkspaceRoot(), additional...)
	filtered := make([]ExtensionInfo, 0, len(all))
	for _, ext := range all {
		if a.ProjectResourceAllowed(ext.Path) {
			filtered = append(filtered, ext)
		}
	}
	return filtered
}

func RunExtension(
	a *agent.Agent,
	cmdName string,
	args []string,
	messages *[]db.Message,
	w io.Writer,
) (bool, error) {
	name := strings.TrimPrefix(cmdName, "/")
	if !tool.IsSafeIdentifier(name) {
		return false, nil
	}

	targetCmd := "/" + strings.ToLower(name)
	var additional []string
	if a != nil && a.Config != nil {
		additional = a.Config.ExtensionDirectories
	}
	var extPath string
	blockedProjectExt := false
	for _, ext := range ListExtensions(a.GetWorkspaceRoot(), additional...) {
		if ext.Name != targetCmd {
			continue
		}
		if a == nil || a.ProjectResourceAllowed(ext.Path) {
			extPath = ext.Path
			break
		}
		blockedProjectExt = true
	}

	if extPath == "" {
		if blockedProjectExt {
			return true, fmt.Errorf("project extensions are disabled for this untrusted workspace (run with trust to enable)")
		}
		return false, nil
	}

	parentCtx := context.Background()
	if a != nil {
		parentCtx = a.Context()
	}
	var history []db.Message
	if messages != nil {
		if a != nil {
			a.MessagesMu.RLock()
			history = append([]db.Message(nil), (*messages)...)
			a.MessagesMu.RUnlock()
		} else {
			history = *messages
		}
	}
	timeout := 15 * time.Second
	if a != nil && a.Config != nil && a.Config.ExtensionTimeout > 0 {
		timeout = time.Duration(a.Config.ExtensionTimeout) * time.Second
	}
	output, err := resources.RunCommand(parentCtx, resources.Command{Name: targetCmd, Path: extPath}, args, a.GetWorkspaceRoot(), history, timeout)
	if output != "" {
		if _, writeErr := io.WriteString(w, output); writeErr != nil {
			return true, writeErr
		}
	}
	if err != nil {
		return true, err
	}

	return true, nil
}
