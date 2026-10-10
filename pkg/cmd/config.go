package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/ui"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage loop runtime settings",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Display the current configuration settings card",
	Run:   runRenderCmd(ui.RenderConfig),
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the interactive TUI configuration editor",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrPrintErr()
		if cfg == nil {
			return
		}
		theme := ui.GetConfiguredTheme(cfg)

		newConfig, err := ui.RunInteractiveConfig(cfg, theme, os.Stdin, os.Stdout)
		if err == nil && newConfig != nil {
			_ = config.SaveConfig(configPath, newConfig)
			fmt.Printf("Configuration successfully updated and saved to %s\n", configPath)
		} else {
			fmt.Println("interactive configuration cancelled.")
		}
	},
}

func init() {
	configCmd.AddCommand(configShowCmd, configEditCmd)
	rootCmd.AddCommand(configCmd)
}
