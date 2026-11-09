package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/ui"
	"loop/pkg/ui/render"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage loop runtime settings",
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Display the current configuration settings card",
	RunE:  runRenderCmd(render.RenderConfig),
}

var configEditCmd = &cobra.Command{
	Use:   "edit",
	Short: "Open the interactive TUI configuration editor",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		theme := ui.GetConfiguredTheme(cfg)

		newConfig, err := ui.RunInteractiveConfig(cfg, theme, os.Stdin, os.Stdout)
		if err != nil {
			return fmt.Errorf("edit configuration: %w", err)
		}
		if newConfig == nil {
			return nil
		}
		if err := config.SaveConfigContext(cmd.Context(), configPath, newConfig); err != nil {
			return err
		}
		fmt.Printf("Configuration successfully updated and saved to %s\n", configPath)
		return nil
	},
}

func init() {
	configCmd.AddCommand(configShowCmd, configEditCmd)
	rootCmd.AddCommand(configCmd)
}
