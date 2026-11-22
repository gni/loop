package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"

	"loop/pkg/config"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/terminal"
	"loop/pkg/ui/interactive"
	"loop/pkg/ui/style"
)

func validateMCPURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("enter an HTTP or HTTPS server URL")
	}
	return nil
}

func addMCPServer(cfg *config.Config, sr *interactive.SessionReader, signals chan os.Signal, output io.Writer, theme style.UITheme) (string, error) {
	name, err := interactive.ReadMenuField(sr, signals, output, theme, "Add MCP server", "Give this connection a short name.", "Server name", "", func(value string) error {
		if !domaintool.IsSafeIdentifier(value) {
			return fmt.Errorf("use letters, numbers, underscores or hyphens")
		}
		if _, exists := cfg.MCPServers[value]; exists {
			return fmt.Errorf("a server named %q already exists", value)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	choice, err := interactive.RunInteractiveSelect(sr, signals, output, "Add "+interactive.MenuDisplayText(name)+" / Connection", []string{"Remote server (HTTP)", "Local command (stdio)"}, theme)
	if err != nil {
		return "", &interactive.MenuInputError{Cause: err}
	}
	server := config.MCPServerConfig{Enabled: true}
	if choice == 0 {
		server.URL, err = interactive.ReadMenuField(sr, signals, output, theme, "Add "+interactive.MenuDisplayText(name), "Enter the server address. Headers can be added in Configure.", "Server URL", "", validateMCPURL)
	} else {
		server.Type = "stdio"
		server.Command, err = interactive.ReadMenuField(sr, signals, output, theme, "Add "+interactive.MenuDisplayText(name), "Arguments can be added in Configure.", "Executable", "", func(value string) error {
			if value == "" {
				return fmt.Errorf("enter an executable name or path")
			}
			return nil
		})
	}
	if err != nil {
		return "", err
	}
	cfg.MCPServers[name] = server
	return name, nil
}

func editMCPServer(cfg *config.Config, name string, sr *interactive.SessionReader, output io.Writer, theme style.UITheme) error {
	signals := make(chan os.Signal, 1)
	terminal.NotifyResize(signals)
	defer signal.Stop(signals)
	// Field edits update the draft only. Input cancellation leaves its field intact.
	items := func() []*interactive.SettingItem {
		server := cfg.MCPServers[name]
		result := []*interactive.SettingItem{{ID: "enabled", Name: "Server enabled", IsBool: true,
			Value:       func() string { return fmt.Sprint(cfg.MCPServers[name].Enabled) },
			Description: "Space toggles this server. Changes apply when you close the MCP manager.",
			OnToggle: func() {
				current := cfg.MCPServers[name]
				current.Enabled = !current.Enabled
				cfg.MCPServers[name] = current
			},
		}}
		if mcpLocal(server) {
			result = append(result, &interactive.SettingItem{ID: "command", Name: "Executable", Value: func() string { return cfg.MCPServers[name].Command },
				Description: "Program to launch. Arguments are configured separately.",
				OnEdit: func(value string) error {
					if value == "" {
						return fmt.Errorf("enter an executable name or path")
					}
					current := cfg.MCPServers[name]
					current.Command = value
					cfg.MCPServers[name] = current
					return nil
				}},
				&interactive.SettingItem{ID: "args", Name: "Arguments", Value: func() string { return fmt.Sprintf("%d values", len(cfg.MCPServers[name].Args)) },
					Description: `JSON list, for example ["--port", "3000"]. Ctrl+U clears the field.`,
					OnAction: interactive.MenuAction(func() error {
						data, _ := json.Marshal(cfg.MCPServers[name].Args)
						if string(data) == "null" {
							data = []byte("[]")
						}
						_, err := interactive.ReadMenuField(sr, signals, output, theme, "Arguments / "+interactive.MenuDisplayText(name), "Enter a JSON list of command arguments. Ctrl+U clears the field.", "Arguments", string(data), func(value string) error {
							var args []string
							if err := json.Unmarshal([]byte(value), &args); err != nil {
								return fmt.Errorf("enter a JSON list of strings")
							}
							current := cfg.MCPServers[name]
							current.Args = args
							cfg.MCPServers[name] = current
							return nil
						})
						return err
					})},
				&interactive.SettingItem{ID: "env", Name: "Environment", Value: func() string { return fmt.Sprintf("%d values (hidden)", len(cfg.MCPServers[name].Env)) },
					Description: "Add, edit or remove environment variables. Values stay hidden.",
					OnAction:    interactive.MenuAction(func() error { return editMCPEnvironment(cfg, name, sr, output, theme) }),
				})
		} else {
			result = append(result, &interactive.SettingItem{ID: "url", Name: "Server URL", Value: func() string { return mcpAddress(cfg.MCPServers[name]) },
				Description: "Remote HTTP or SSE address. Ctrl+U replaces the field.",
				OnAction: interactive.MenuAction(func() error {
					_, err := interactive.ReadMenuField(sr, signals, output, theme, "Server URL / "+interactive.MenuDisplayText(name), "Ctrl+U clears the field. Esc keeps the current address.", "Server URL", cfg.MCPServers[name].URL, func(value string) error {
						if err := validateMCPURL(value); err != nil {
							return err
						}
						current := cfg.MCPServers[name]
						current.URL = value
						cfg.MCPServers[name] = current
						return nil
					})
					return err
				})})
			keys := make([]string, 0, len(server.Headers))
			for key := range server.Headers {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				result = append(result, &interactive.SettingItem{ID: "header_" + key, Name: "Header: " + key, Value: func() string { return "Set (hidden)" },
					Description: "Edit this header. Clearing its value removes it. Ctrl+U clears the field.",
					OnAction: interactive.MenuAction(func() error {
						_, err := interactive.ReadMenuSecretField(sr, signals, output, theme, "Header / "+interactive.MenuDisplayText(name), "Ctrl+U clears the field. An empty value removes this header.", interactive.MenuDisplayText(key), cfg.MCPServers[name].Headers[key], func(value string) error {
							current := cfg.MCPServers[name]
							if value == "" {
								delete(current.Headers, key)
							} else {
								current.Headers[key] = value
							}
							cfg.MCPServers[name] = current
							return nil
						})
						return err
					})})
			}
			result = append(result, &interactive.SettingItem{ID: "add_header", Name: "Add HTTP header", Value: func() string { return "Enter" },
				Description: "Add an authorization or custom header. Its value stays hidden.",
				OnAction: interactive.MenuAction(func() error {
					key, err := interactive.ReadMenuField(sr, signals, output, theme, "Add header / "+interactive.MenuDisplayText(name), "For example Authorization or X-Api-Key.", "Header name", "", func(value string) error {
						if value == "" {
							return fmt.Errorf("enter a header name")
						}
						for _, ch := range value {
							if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", ch)) {
								return fmt.Errorf("invalid HTTP header name")
							}
						}
						return nil
					})
					if err != nil {
						return err
					}
					value, err := interactive.ReadMenuSecretField(sr, signals, output, theme, "Add header / "+interactive.MenuDisplayText(name), "The value is hidden while you type.", key, "", nil)
					if err != nil {
						return err
					}
					current := cfg.MCPServers[name]
					if current.Headers == nil {
						current.Headers = make(map[string]string)
					}
					current.Headers[key] = value
					cfg.MCPServers[name] = current
					return nil
				})})
		}
		result = append(result, &interactive.SettingItem{ID: "startup", Name: "Connect at startup", IsBool: true,
			Value:       func() string { return fmt.Sprint(cfg.MCPServers[name].AlwaysLoad) },
			Description: "Off connects only when a tool needs this server.",
			OnToggle: func() {
				current := cfg.MCPServers[name]
				current.AlwaysLoad = !current.AlwaysLoad
				cfg.MCPServers[name] = current
			},
		})
		return result
	}
	return interactive.RunSettingsMenuLoop(sr, output, theme, "MCP / "+interactive.MenuDisplayText(name), items, nil)
}
