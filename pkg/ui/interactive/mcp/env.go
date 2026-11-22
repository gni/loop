package mcp

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

func editMCPEnvironment(cfg *config.Config, name string, sr *interactive.SessionReader, output io.Writer, theme style.UITheme) error {
	signals := make(chan os.Signal, 1)
	terminal.NotifyResize(signals)
	defer signal.Stop(signals)
	set := func(key, value string) {
		server := cfg.MCPServers[name]
		if server.Env == nil {
			server.Env = make(map[string]string)
		}
		server.Env[key] = value
		cfg.MCPServers[name] = server
	}
	field := func(key string) error {
		_, err := interactive.ReadMenuSecretField(sr, signals, output, theme, "Environment / "+interactive.MenuDisplayText(name), "Values stay hidden. Ctrl+U clears the field; an empty value is allowed.", interactive.MenuDisplayText(key), cfg.MCPServers[name].Env[key], func(value string) error {
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("environment values cannot contain NUL")
			}
			set(key, value)
			return nil
		})
		return err
	}

	items := func() []*interactive.SettingItem {
		keys := make([]string, 0, len(cfg.MCPServers[name].Env))
		for key := range cfg.MCPServers[name].Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var result []*interactive.SettingItem
		for _, key := range keys {
			result = append(result, &interactive.SettingItem{ID: key, Name: key, Value: func() string {
				if cfg.MCPServers[name].Env[key] == "" {
					return "Empty"
				}
				return "Set (hidden)"
			}, Description: "Enter edits this value. Ctrl+U clears the field.", OnAction: interactive.MenuAction(func() error { return field(key) })})
		}
		result = append(result, &interactive.SettingItem{ID: "add_variable", Name: "Add variable", Value: func() string { return "Enter" },
			Description: "Add an environment variable for this command.",
			OnAction: interactive.MenuAction(func() error {
				key, err := interactive.ReadMenuField(sr, signals, output, theme, "Add variable / "+interactive.MenuDisplayText(name), "For example API_KEY or DATA_DIR.", "Variable name", "", func(key string) error {
					if key == "" || strings.ContainsAny(key, "=\x00") {
						return fmt.Errorf("enter a valid environment name")
					}
					if _, exists := cfg.MCPServers[name].Env[key]; exists {
						return fmt.Errorf("this variable already exists")
					}
					return nil
				})
				if err != nil {
					return err
				}
				return field(key)
			})})
		if len(keys) > 0 {
			result = append(result, &interactive.SettingItem{ID: "remove_variable", Name: "Remove variable", Value: func() string { return "Enter" },
				Description: "Choose an environment variable to remove.",
				OnAction: interactive.MenuAction(func() error {
					index, err := interactive.RunInteractiveSelect(sr, signals, output, "Remove variable / "+interactive.MenuDisplayText(name), keys, theme)
					if err != nil {
						return &interactive.MenuInputError{Cause: err}
					}
					server := cfg.MCPServers[name]
					delete(server.Env, keys[index])
					cfg.MCPServers[name] = server
					return nil
				})})
		}
		return result
	}
	return interactive.RunSettingsMenuLoop(sr, output, theme, "Environment / "+interactive.MenuDisplayText(name), items, nil)
}
