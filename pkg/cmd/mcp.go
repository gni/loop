package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/ui"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP server configurations",
}

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured MCP servers",
	Run:   runRenderCmd(ui.RenderMCPServers),
}

var mcpAddCmd = &cobra.Command{
	Use:   "add <name> <url> [headerKey:headerVal]...",
	Short: "Add a new MCP server configuration",
	Args:  cobra.MinimumNArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrPrintErr()
		if cfg == nil {
			return
		}
		name := args[0]
		url := args[1]
		headers := config.ParseHeaderArgs(args[2:])

		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]config.MCPServerConfig)
		}
		cfg.MCPServers[name] = config.MCPServerConfig{
			URL:     url,
			Headers: headers,
		}
		err := config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("MCP Server '%s' successfully added/updated.\n", name)
	},
}

var mcpRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrPrintErr()
		if cfg == nil {
			return
		}
		name := args[0]
		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]config.MCPServerConfig)
		}
		if _, ok := cfg.MCPServers[name]; !ok {
			fmt.Printf("Error: MCP server '%s' not found.\n", name)
			return
		}

		delete(cfg.MCPServers, name)
		err := config.SaveConfig(configPath, cfg)
		if err != nil {
			fmt.Printf("Error saving config: %v\n", err)
			return
		}
		fmt.Printf("MCP Server '%s' removed.\n", name)
	},
}

func init() {
	mcpCmd.AddCommand(mcpListCmd, mcpAddCmd, mcpRemoveCmd)
	rootCmd.AddCommand(mcpCmd)
}
