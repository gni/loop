package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"loop/pkg/process"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxPluginOutputBytes     = 1 << 20
	maxPluginErrorBytes      = 32 << 10
	maxPluginDescriptorBytes = 64 << 10
)

type pluginTool struct {
	path string
	def  Tool
}

func (p *pluginTool) Name() string {
	return p.def.Function.Name
}

func (p *pluginTool) Definition() Tool {
	return p.def
}

func (p *pluginTool) Execute(ctx AgentContext, arguments string) (string, error) {
	absPath, err := filepath.Abs(p.path)
	if err != nil {
		return "", fmt.Errorf("invalid plugin path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("plugin file error: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("plugin path is a directory")
	}

	parent := context.Background()
	if ctx != nil && ctx.Context() != nil {
		parent = ctx.Context()
	}
	result, err := process.Run(parent, process.Options{Executable: absPath, Input: strings.NewReader(arguments), Timeout: 10 * time.Second, OutputLimit: maxPluginOutputBytes, ErrorLimit: maxPluginErrorBytes})
	if err != nil {
		return result.Stdout, fmt.Errorf("plugin execution failed: %w (details: %s)", err, strings.TrimSpace(result.Stderr))
	}
	return result.Stdout, nil
}

func dedupeExistingDirs(candidates []string) []string {
	seen := make(map[string]bool, len(candidates))
	var dirs []string
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil || seen[abs] {
			continue
		}
		seen[abs] = true
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			dirs = append(dirs, abs)
		}
	}
	return dirs
}

func PluginSearchDirs(workspaceRoot string) []string {
	var candidates []string
	if workspaceRoot != "" {
		candidates = append(candidates,
			filepath.Join(workspaceRoot, ".loop", "plugins"),
			filepath.Join(workspaceRoot, "plugins"),
			filepath.Join(workspaceRoot, "addons", "plugins"),
			filepath.Join(workspaceRoot, "addons"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".loop", "plugins"),
			filepath.Join(home, ".loop", "addons", "plugins"),
			filepath.Join(home, ".loop", "addons"),
		)
	}

	return dedupeExistingDirs(candidates)
}

func ExtensionSearchDirs(workspaceRoot string) []string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".loop", "extensions"),
			filepath.Join(home, ".loop", "addons", "extensions"),
			filepath.Join(home, ".loop", "addons"),
		)
	}
	if workspaceRoot != "" {
		candidates = append(candidates,
			filepath.Join(workspaceRoot, ".loop", "extensions"),
			filepath.Join(workspaceRoot, "extensions"),
			filepath.Join(workspaceRoot, "addons", "extensions"),
			filepath.Join(workspaceRoot, "addons"),
		)
	}

	return dedupeExistingDirs(candidates)
}

func LocalAddonDirs(workspaceRoot string) []string {
	if workspaceRoot == "" {
		return nil
	}
	return []string{
		filepath.Join(workspaceRoot, ".loop", "plugins"),
		filepath.Join(workspaceRoot, "plugins"),
		filepath.Join(workspaceRoot, ".loop", "extensions"),
		filepath.Join(workspaceRoot, "extensions"),
		filepath.Join(workspaceRoot, "addons"),
		filepath.Join(workspaceRoot, "addons", "plugins"),
		filepath.Join(workspaceRoot, "addons", "extensions"),
	}
}

func HasPluginDescriptor(pluginPath string) (string, bool) {
	jsonPath1 := pluginPath + ".json"
	if _, err := os.Stat(jsonPath1); err == nil {
		return jsonPath1, true
	}
	baseWithoutExt := strings.TrimSuffix(pluginPath, filepath.Ext(pluginPath))
	jsonPath2 := baseWithoutExt + ".json"
	if _, err := os.Stat(jsonPath2); err == nil {
		return jsonPath2, true
	}
	return "", false
}

func RegisterPlugins(registry *ToolRegistry, dir string) error {
	return RegisterPluginsContext(context.Background(), registry, dir)
}

func RegisterPluginsContext(ctx context.Context, registry *ToolRegistry, dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return err
	}

	isDedicatedPluginDir := strings.EqualFold(filepath.Base(absDir), "plugins")

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("plugin registration interrupted: %w", err)
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) == ".json" {
			continue
		}

		pluginPath := filepath.Join(absDir, entry.Name())
		info, err := os.Stat(pluginPath)
		if err != nil || info.Mode()&0111 == 0 {
			continue
		}

		var jsonBytes []byte
		if descPath, ok := HasPluginDescriptor(pluginPath); ok {
			file, openErr := os.Open(descPath)
			if openErr != nil {
				continue
			}
			jsonBytes, err = io.ReadAll(io.LimitReader(file, maxPluginDescriptorBytes+1))
			_ = file.Close()
			if err != nil || len(jsonBytes) > maxPluginDescriptorBytes {
				continue
			}
		} else if isDedicatedPluginDir {
			result, runErr := process.Run(ctx, process.Options{Executable: pluginPath, Arguments: []string{"--info"}, Timeout: 5 * time.Second, OutputLimit: maxPluginDescriptorBytes})
			if runErr == nil {
				jsonBytes = []byte(result.Stdout)
			}
		}

		if len(jsonBytes) == 0 {
			continue
		}

		var functionDef FunctionDefinition
		if err := json.Unmarshal(jsonBytes, &functionDef); err != nil || functionDef.Name == "" {
			continue
		}

		if !strings.HasPrefix(functionDef.Name, "plugin__") {
			functionDef.Name = "plugin__" + functionDef.Name
		}

		if !IsSafeIdentifier(functionDef.Name) {
			continue
		}

		if _, exists := registry.Lookup(functionDef.Name); exists {
			return fmt.Errorf("duplicate plugin tool %s in %s", functionDef.Name, pluginPath)
		}
		registry.Register(&pluginTool{
			path: pluginPath,
			def: Tool{
				Type:     "function",
				Function: functionDef,
			},
		})
	}

	return nil
}
