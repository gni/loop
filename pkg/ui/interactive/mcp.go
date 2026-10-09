package interactive

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"golang.org/x/term"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RunInteractiveMCPConfig(
	cfg *config.Config,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
	configPath string,
) (*config.Config, error) {
	var fd int
	if f, ok := rlInput.(*os.File); ok {
		fd = int(f.Fd())
	} else {
		fd = int(os.Stdin.Fd())
	}

	if !term.IsTerminal(fd) {
		return cfg, nil
	}

	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer term.Restore(fd, oldState)

	fmt.Fprint(rlOutput, terminal.EnterAlternateScreen)
	fmt.Fprint(rlOutput, "\x1b[?25l")
	defer func() {
		fmt.Fprint(rlOutput, "\x1b[?25h")
		fmt.Fprint(rlOutput, terminal.ExitAlternateScreen)
	}()

	cloned := *cfg
	if cloned.MCPServers == nil {
		cloned.MCPServers = make(map[string]config.MCPServerConfig)
	}

	selectedServer := ""

	// Helper to get first key alphabetically
	getFirstServer := func() string {
		if len(cloned.MCPServers) == 0 {
			return ""
		}
		var keys []string
		for k := range cloned.MCPServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys[0]
	}

	selectedServer = getFirstServer()

	itemsProvider := func() []*settingItem {
		// Ensure selectedServer is valid if servers exist
		if selectedServer != "" {
			if _, exists := cloned.MCPServers[selectedServer]; !exists {
				selectedServer = getFirstServer()
			}
		} else if len(cloned.MCPServers) > 0 {
			selectedServer = getFirstServer()
		}

		var items []*settingItem
		items = []*settingItem{
			{
				id:   "selected_server",
				name: "selected mcp server",
				value: func() string {
					if selectedServer == "" {
						return "none"
					}
					return selectedServer
				},
				description: "Currently selected MCP server configuration to view/edit",
				onToggle: func() {
					if len(cloned.MCPServers) == 0 {
						return
					}
					var keys []string
					for k := range cloned.MCPServers {
						keys = append(keys, k)
					}
					sort.Strings(keys)

					selectedServer = cycleOption(keys, selectedServer)
				},
			},
		}

		if selectedServer != "" {
			srvName := selectedServer
			cfgServer := cloned.MCPServers[srvName]

			items = append(items, &settingItem{
				id:          "server_url",
				name:        "server url",
				value:       func() string { return cloned.MCPServers[srvName].URL },
				description: "The HTTP/SSE URL of the MCP server",
				onEdit: func(newVal string) error {
					if newVal == "" {
						return fmt.Errorf("url cannot be empty")
					}
					curr := cloned.MCPServers[srvName]
					curr.URL = newVal
					cloned.MCPServers[srvName] = curr
					return nil
				},
			})

			items = append(items, &settingItem{
				id:   "server_enabled",
				name: "status",
				value: func() string {
					if cloned.MCPServers[srvName].Disabled {
						return "\x1b[31mdisabled\x1b[0m"
					} else {
						return "\x1b[32menabled\x1b[0m"
					}
				},
				description: fmt.Sprintf("Toggle to enable or disable MCP server '%s'", srvName),
				onToggle: func() {
					curr := cloned.MCPServers[srvName]
					curr.Disabled = !curr.Disabled
					cloned.MCPServers[srvName] = curr
				},
			})

			// Extract and sort headers
			var hKeys []string
			for h := range cfgServer.Headers {
				hKeys = append(hKeys, h)
			}
			sort.Strings(hKeys)

			for _, hk := range hKeys {
				headerName := hk
				items = append(items, &settingItem{
					id:          "header_" + headerName,
					name:        "  header: " + headerName,
					value:       func() string { return cloned.MCPServers[srvName].Headers[headerName] },
					description: fmt.Sprintf("HTTP Header '%s' value for '%s'", headerName, srvName),
					onEdit: func(newVal string) error {
						curr := cloned.MCPServers[srvName]
						if newVal == "" {
							delete(curr.Headers, headerName)
						} else {
							curr.Headers[headerName] = newVal
						}
						cloned.MCPServers[srvName] = curr
						return nil
					},
				})
			}

			items = append(items, &settingItem{
				id:          "action_add_header",
				name:        "  [ add HTTP header ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Add a custom HTTP header for '%s'", srvName),
				onToggle: func() {
					fmt.Fprint(rlOutput, "\r\n\r\n  === Add Header ===\r\n")
					fmt.Fprint(rlOutput, "  Enter header name (e.g. Authorization, X-Api-Key): ")
					hName, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					hName = strings.TrimSpace(hName)
					if hName != "" {
						fmt.Fprintf(rlOutput, "  Enter value for '%s': ", hName)
						hVal, err := readInputRaw(rlInput, rlOutput)
						if err != nil {
							return
						}
						hVal = strings.TrimSpace(hVal)

						curr := cloned.MCPServers[srvName]
						if curr.Headers == nil {
							curr.Headers = make(map[string]string)
						}
						curr.Headers[hName] = hVal
						cloned.MCPServers[srvName] = curr
					}
				},
			})

			items = append(items, &settingItem{
				id:          "action_remove_server",
				name:        "[ remove selected server ]",
				value:       func() string { return "" },
				description: fmt.Sprintf("Delete the MCP server configuration for '%s'", srvName),
				onToggle: func() {
					delete(cloned.MCPServers, srvName)
					selectedServer = getFirstServer()
				},
			})
		}

		// Global MCP Actions
		items = append(items, &settingItem{
			id:          "action_add_server",
			name:        "[ add new mcp server ]",
			value:       func() string { return "" },
			description: "Add a new MCP server configuration by entering name and URL",
			onToggle: func() {
				fmt.Fprint(rlOutput, "\r\n\r\n  === Add New MCP Server ===\r\n")
				fmt.Fprint(rlOutput, "  Enter server name: ")
				sName, err := readInputRaw(rlInput, rlOutput)
				if err != nil {
					return
				}
				sName = strings.TrimSpace(sName)

				if sName != "" {
					fmt.Fprint(rlOutput, "  Enter SSE endpoint URL: ")
					sURL, err := readInputRaw(rlInput, rlOutput)
					if err != nil {
						return
					}
					sURL = strings.TrimSpace(sURL)

					if sURL != "" {
						if cloned.MCPServers == nil {
							cloned.MCPServers = make(map[string]config.MCPServerConfig)
						}
						cloned.MCPServers[sName] = config.MCPServerConfig{
							URL:     sURL,
							Headers: make(map[string]string),
						}
						selectedServer = sName
					}
				}
			},
		})

		return items
	}

	err = runSettingsMenuLoop(rlInput, rlOutput, theme, "mcp servers settings setup", itemsProvider, nil)
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}
