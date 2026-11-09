package interactive

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func editMCPEnvironment(cfg *config.Config, name string, sr *SessionReader, output io.Writer, theme style.UITheme) error {
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
		_, err := readMenuSecretField(sr, signals, output, theme, "Environment / "+menuDisplayText(name), "Values stay hidden. Ctrl+U clears the field; an empty value is allowed.", menuDisplayText(key), cfg.MCPServers[name].Env[key], func(value string) error {
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("environment values cannot contain NUL")
			}
			set(key, value)
			return nil
		})
		return err
	}

	items := func() []*settingItem {
		keys := make([]string, 0, len(cfg.MCPServers[name].Env))
		for key := range cfg.MCPServers[name].Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var result []*settingItem
		for _, key := range keys {
			result = append(result, &settingItem{id: key, name: key, value: func() string {
				if cfg.MCPServers[name].Env[key] == "" {
					return "Empty"
				}
				return "Set (hidden)"
			}, description: "Enter edits this value. Ctrl+U clears the field.", onAction: menuAction(func() error { return field(key) })})
		}
		result = append(result, &settingItem{id: "add_variable", name: "Add variable", value: func() string { return "Enter" },
			description: "Add an environment variable for this command.",
			onAction: menuAction(func() error {
				key, err := readMenuField(sr, signals, output, theme, "Add variable / "+menuDisplayText(name), "For example API_KEY or DATA_DIR.", "Variable name", "", func(key string) error {
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
			result = append(result, &settingItem{id: "remove_variable", name: "Remove variable", value: func() string { return "Enter" },
				description: "Choose an environment variable to remove.",
				onAction: menuAction(func() error {
					index, err := RunInteractiveSelect(sr, signals, output, "Remove variable / "+menuDisplayText(name), keys, theme)
					if err != nil {
						return &menuInputError{cause: err}
					}
					server := cfg.MCPServers[name]
					delete(server.Env, keys[index])
					cfg.MCPServers[name] = server
					return nil
				})})
		}
		return result
	}
	return runSettingsMenuLoop(sr, output, theme, "Environment / "+menuDisplayText(name), items, nil)
}
