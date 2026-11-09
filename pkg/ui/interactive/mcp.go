package interactive

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"loop/pkg/config"
	"loop/pkg/terminal"
	"loop/pkg/ui/style"
)

func RunInteractiveMCPConfig(
	cfg *config.Config,
	theme style.UITheme,
	rlInput io.Reader,
	rlOutput io.Writer,
	reconnect func(*config.Config) error,
) (*config.Config, error) {
	clonedPtr, cloneErr := config.Clone(cfg)
	if cloneErr != nil || clonedPtr == nil {
		return nil, fmt.Errorf("mcp configuration is unavailable")
	}
	cloned := *clonedPtr
	if cloned.MCPServers == nil {
		cloned.MCPServers = make(map[string]config.MCPServerConfig)
	}

	isTTY, err := withAltScreenTerminal(rlInput, rlOutput, func() error {
		sr := NewSessionReader(rlInput)
		defer sr.Close()

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

		selectedServer := getFirstServer()
		reconnectNotice := ""
		reconnectFailed := false

		itemsProvider := func() []*settingItem {
			if selectedServer != "" {
				if _, exists := cloned.MCPServers[selectedServer]; !exists {
					selectedServer = getFirstServer()
				}
			} else if len(cloned.MCPServers) > 0 {
				selectedServer = getFirstServer()
			}

			items := []*settingItem{
				boolConfigItem("mcp", "MCP enabled (all servers)", "Space or Enter toggles all MCP connections and tools. Esc saves and applies. Server settings are kept.", &cloned),
				{
					id: "action_reconnect", name: "[ reconnect enabled servers ]",
					value: func() string {
						if reconnectNotice != "" {
							return reconnectNotice
						}
						return "Enter to reconnect"
					},
					description: "Save current settings, reconnect enabled servers and refresh their tools.",
					onToggle: func() {
						reconnectFailed = true
						if reconnect == nil {
							reconnectNotice = "Reconnect is unavailable."
							return
						}
						if !cloned.MCP {
							reconnectNotice = "Enable MCP before reconnecting."
							return
						}
						enabled := 0
						for _, server := range cloned.MCPServers {
							if server.Enabled {
								enabled++
							}
						}
						if enabled == 0 {
							reconnectNotice = "Enable at least one server before reconnecting."
							return
						}
						if err := writeMenuFrame(rlOutput, "\x1b[H\x1b[JReconnecting MCP servers...\r\n"); err != nil {
							reconnectNotice = err.Error()
							return
						}
						if err := reconnect(&cloned); err != nil {
							reconnectNotice = err.Error()
							return
						}
						reconnectFailed = false
						reconnectNotice = fmt.Sprintf("Reconnected %d enabled server(s). Tools refreshed.", enabled)
					},
				},
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

				items = append(items,
					&settingItem{
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
					},
					&settingItem{
						id:   "server_enabled",
						name: "selected server enabled",
						value: func() string {
							if !cloned.MCPServers[srvName].Enabled {
								return "\x1b[31mdisabled\x1b[0m"
							}
							return "\x1b[32menabled\x1b[0m"
						},
						isBool:      true,
						description: fmt.Sprintf("Toggle to enable or disable MCP server '%s'. The master MCP switch must also be enabled.", srvName),
						onToggle: func() {
							curr := cloned.MCPServers[srvName]
							curr.Enabled = !(curr.Enabled)
							cloned.MCPServers[srvName] = curr
						},
					},
				)

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

				items = append(items,
					&settingItem{
						id:          "action_add_header",
						name:        "  [ add HTTP header ]",
						value:       func() string { return "" },
						description: fmt.Sprintf("Add a custom HTTP header for '%s'", srvName),
						onToggle: func() {
							fmt.Fprint(rlOutput, "\r\n\r\n  === Add Header ===\r\n")
							fmt.Fprint(rlOutput, "  Enter header name (e.g. Authorization, X-Api-Key): ")
							hName, err := ReadInputRaw(sr, rlOutput)
							if err != nil {
								return
							}
							hName = strings.TrimSpace(hName)
							if hName != "" {
								fmt.Fprintf(rlOutput, "  Enter value for '%s': ", hName)
								hVal, err := ReadInputRaw(sr, rlOutput)
								if err != nil {
									return
								}
								curr := cloned.MCPServers[srvName]
								if curr.Headers == nil {
									curr.Headers = make(map[string]string)
								}
								curr.Headers[hName] = strings.TrimSpace(hVal)
								cloned.MCPServers[srvName] = curr
							}
						},
					},
					&settingItem{
						id:          "action_remove_server",
						name:        "[ remove selected server ]",
						value:       func() string { return "" },
						description: fmt.Sprintf("Delete the MCP server configuration for '%s'", srvName),
						onToggle: func() {
							delete(cloned.MCPServers, srvName)
							selectedServer = getFirstServer()
						},
					},
				)
			}

			items = append(items, &settingItem{
				id:          "action_add_server",
				name:        "[ add new mcp server ]",
				value:       func() string { return "" },
				description: "Add a new MCP server configuration by entering name and URL",
				onToggle: func() {
					fmt.Fprint(rlOutput, "\r\n\r\n  === Add New MCP Server ===\r\n")
					fmt.Fprint(rlOutput, "  Enter server name: ")
					sName, err := ReadInputRaw(sr, rlOutput)
					if err != nil {
						return
					}
					sName = strings.TrimSpace(sName)
					if sName != "" {
						if _, exists := cloned.MCPServers[sName]; exists {
							fmt.Fprint(rlOutput, "  Server already exists. Press enter to continue...")
							_, _ = ReadInputRaw(sr, rlOutput)
							return
						}
						fmt.Fprint(rlOutput, "  Enter SSE endpoint URL: ")
						sURL, err := ReadInputRaw(sr, rlOutput)
						if err != nil {
							return
						}
						sURL = strings.TrimSpace(sURL)
						if sURL != "" {
							if cloned.MCPServers == nil {
								cloned.MCPServers = make(map[string]config.MCPServerConfig)
							}
							cloned.MCPServers[sName] = config.MCPServerConfig{
								Enabled: true,
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

		extraRender := func(buf *strings.Builder) {
			state, tint := "OFF", theme.Warning
			message := "Connections and tools are disabled. Server settings are kept."
			if cloned.MCP {
				state, tint = "ON", theme.Success
				message = "Enabled servers can connect and expose tools."
			}
			buf.WriteString("  " + style.NewStyle().Foreground(tint).Bold(true).Render("MCP "+state) + "\n")
			width, _ := terminal.GetDimensions()
			if reconnectNotice != "" {
				message = reconnectNotice
				tint = theme.Success
				if reconnectFailed {
					tint = theme.Error
				}
			} else {
				tint = theme.TextMuted
			}
			buf.WriteString("  " + style.NewStyle().Foreground(tint).Render(style.TruncateCells(message, max(1, width-3))) + "\n")
		}
		return runSettingsMenuLoop(sr, rlOutput, theme, "MCP", itemsProvider, extraRender)
	})
	if !isTTY {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	return &cloned, nil
}
