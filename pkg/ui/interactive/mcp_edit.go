package interactive

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
	"loop/pkg/ui/style"
)

func validateMCPURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("enter an HTTP or HTTPS server URL")
	}
	return nil
}

func addMCPServer(cfg *config.Config, sr *SessionReader, signals chan os.Signal, output io.Writer, theme style.UITheme) (string, error) {
	name, err := readMenuField(sr, signals, output, theme, "Add MCP server", "Give this connection a short name.", "Server name", "", func(value string) error {
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
	choice, err := RunInteractiveSelect(sr, signals, output, "Add "+menuDisplayText(name)+" / Connection", []string{"Remote server (HTTP)", "Local command (stdio)"}, theme)
	if err != nil {
		return "", &menuInputError{cause: err}
	}
	server := config.MCPServerConfig{Enabled: true}
	if choice == 0 {
		server.URL, err = readMenuField(sr, signals, output, theme, "Add "+menuDisplayText(name), "Enter the server address. Headers can be added in Configure.", "Server URL", "", validateMCPURL)
	} else {
		server.Type = "stdio"
		server.Command, err = readMenuField(sr, signals, output, theme, "Add "+menuDisplayText(name), "Arguments can be added in Configure.", "Executable", "", func(value string) error {
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

func editMCPServer(cfg *config.Config, name string, sr *SessionReader, output io.Writer, theme style.UITheme) error {
	signals := make(chan os.Signal, 1)
	terminal.NotifyResize(signals)
	defer signal.Stop(signals)
	// Field edits update the draft only. Input cancellation leaves its field intact.
	items := func() []*settingItem {
		server := cfg.MCPServers[name]
		result := []*settingItem{{id: "enabled", name: "Server enabled", isBool: true,
			value:       func() string { return fmt.Sprint(cfg.MCPServers[name].Enabled) },
			description: "Space toggles this server. Changes apply when you close the MCP manager.",
			onToggle: func() {
				current := cfg.MCPServers[name]
				current.Enabled = !current.Enabled
				cfg.MCPServers[name] = current
			},
		}}
		if mcpLocal(server) {
			result = append(result, &settingItem{id: "command", name: "Executable", value: func() string { return cfg.MCPServers[name].Command },
				description: "Program to launch. Arguments are configured separately.",
				onEdit: func(value string) error {
					if value == "" {
						return fmt.Errorf("enter an executable name or path")
					}
					current := cfg.MCPServers[name]
					current.Command = value
					cfg.MCPServers[name] = current
					return nil
				}},
				&settingItem{id: "args", name: "Arguments", value: func() string { return fmt.Sprintf("%d values", len(cfg.MCPServers[name].Args)) },
					description: `JSON list, for example ["--port", "3000"]. Ctrl+U clears the field.`,
					onAction: menuAction(func() error {
						data, _ := json.Marshal(cfg.MCPServers[name].Args)
						if string(data) == "null" {
							data = []byte("[]")
						}
						_, err := readMenuField(sr, signals, output, theme, "Arguments / "+menuDisplayText(name), "Enter a JSON list of command arguments. Ctrl+U clears the field.", "Arguments", string(data), func(value string) error {
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
				&settingItem{id: "env", name: "Environment", value: func() string { return fmt.Sprintf("%d values (hidden)", len(cfg.MCPServers[name].Env)) },
					description: "Add, edit or remove environment variables. Values stay hidden.",
					onAction:    menuAction(func() error { return editMCPEnvironment(cfg, name, sr, output, theme) }),
				})
		} else {
			result = append(result, &settingItem{id: "url", name: "Server URL", value: func() string { return mcpAddress(cfg.MCPServers[name]) },
				description: "Remote HTTP or SSE address. Ctrl+U replaces the field.",
				onAction: menuAction(func() error {
					_, err := readMenuField(sr, signals, output, theme, "Server URL / "+menuDisplayText(name), "Ctrl+U clears the field. Esc keeps the current address.", "Server URL", cfg.MCPServers[name].URL, func(value string) error {
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
				result = append(result, &settingItem{id: "header_" + key, name: "Header: " + key, value: func() string { return "Set (hidden)" },
					description: "Edit this header. Clearing its value removes it. Ctrl+U clears the field.",
					onAction: menuAction(func() error {
						_, err := readMenuSecretField(sr, signals, output, theme, "Header / "+menuDisplayText(name), "Ctrl+U clears the field. An empty value removes this header.", menuDisplayText(key), cfg.MCPServers[name].Headers[key], func(value string) error {
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
			result = append(result, &settingItem{id: "add_header", name: "Add HTTP header", value: func() string { return "Enter" },
				description: "Add an authorization or custom header. Its value stays hidden.",
				onAction: menuAction(func() error {
					key, err := readMenuField(sr, signals, output, theme, "Add header / "+menuDisplayText(name), "For example Authorization or X-Api-Key.", "Header name", "", func(value string) error {
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
					value, err := readMenuSecretField(sr, signals, output, theme, "Add header / "+menuDisplayText(name), "The value is hidden while you type.", key, "", nil)
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
		result = append(result, &settingItem{id: "startup", name: "Connect at startup", isBool: true,
			value:       func() string { return fmt.Sprint(cfg.MCPServers[name].AlwaysLoad) },
			description: "Off connects only when a tool needs this server.",
			onToggle: func() {
				current := cfg.MCPServers[name]
				current.AlwaysLoad = !current.AlwaysLoad
				cfg.MCPServers[name] = current
			},
		})
		return result
	}
	return runSettingsMenuLoop(sr, output, theme, "MCP / "+menuDisplayText(name), items, nil)
}
