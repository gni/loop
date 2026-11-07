package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/ui/render"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Manage MCP server configurations",
}

var mcpListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured MCP servers",
	RunE:  runRenderCmd(render.RenderMCPServers),
}

var mcpAddCmd = &cobra.Command{
	Use:   "add <name> <url> [headerKey:headerVal]...",
	Short: "Add a new MCP server configuration",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return err
		}
		name := args[0]
		url := args[1]
		headers := config.ParseHeaderArgs(args[2:])

		if cfg.MCPServers == nil {
			cfg.MCPServers = make(map[string]config.MCPServerConfig)
		}
		cfg.MCPServers[name] = config.MCPServerConfig{
			Enabled: true,
			URL:     url,
			Headers: headers,
		}
		if err := config.SaveConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("MCP Server '%s' successfully added/updated.\n", name)
		return nil
	},
}

var mcpRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an MCP server configuration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return err
		}
		name := args[0]
		if _, ok := cfg.MCPServers[name]; !ok {
			return fmt.Errorf("MCP server %q not found", name)
		}

		delete(cfg.MCPServers, name)
		if err := config.SaveConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("MCP Server '%s' removed.\n", name)
		return nil
	},
}

func init() {
	mcpCmd.AddCommand(mcpListCmd, mcpAddCmd, mcpRemoveCmd)
	rootCmd.AddCommand(mcpCmd)
}
