package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	maxPluginOutputBytes = 1 << 20
	maxPluginErrorBytes = 32 << 10
	maxPluginDescriptorBytes = 64 << 10
)

type boundedPluginOutput struct {
	bytes.Buffer
	limit int
	overflow bool
}

func (b *boundedPluginOutput) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining < len(p) {
		b.overflow = true
	}
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return original, nil
}

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
	ctxTimeout, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctxTimeout, absPath)
	cmd.Stdin = strings.NewReader(arguments)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	stdout := &boundedPluginOutput{limit: maxPluginOutputBytes}
	stderr := &boundedPluginOutput{limit: maxPluginErrorBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()
	if ctxTimeout.Err() != nil {
		return "", fmt.Errorf("plugin execution interrupted: %w", ctxTimeout.Err())
	}
	if stdout.overflow {
		return "", fmt.Errorf("plugin output exceeded the %d byte limit", maxPluginOutputBytes)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if stderr.overflow {
			detail += " [stderr truncated]"
		}
		return "", fmt.Errorf("plugin error: %w (details: %s)", err, detail)
	}

	return stdout.String(), nil
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
	if workspaceRoot != "/workspace" {
		candidates = append(candidates,
			"/workspace/plugins",
			"/workspace/addons/plugins",
			"/workspace/addons",
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
			filepath.Join(workspaceRoot, "extensions"),
			filepath.Join(workspaceRoot, "addons", "extensions"),
			filepath.Join(workspaceRoot, "addons"),
		)
	}
	if workspaceRoot != "/workspace" {
		candidates = append(candidates,
			"/workspace/extensions",
			"/workspace/addons/extensions",
			"/workspace/addons",
		)
	}
	return dedupeExistingDirs(candidates)
}

func LocalAddonDirs(workspaceRoot string) []string {
	if workspaceRoot == "" {
		return nil
	}
	return []string{
		filepath.Join(workspaceRoot, "plugins"),
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
			ctxTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
			cmd := exec.CommandContext(ctxTimeout, pluginPath, "--info")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Cancel = func() error {
				if cmd.Process == nil { return os.ErrProcessDone }
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			cmd.WaitDelay = 2 * time.Second
			cmd.Stderr = io.Discard
			out := &boundedPluginOutput{limit: maxPluginDescriptorBytes}
			cmd.Stdout = out
			runErr := cmd.Run()
			cancel()
			if runErr == nil && !out.overflow {
				jsonBytes = out.Bytes()
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
