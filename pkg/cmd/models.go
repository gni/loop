package cmd

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func init() {
	var discover bool
	command := &cobra.Command{Use: "models", Short: "List configured model capabilities and prices", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		workspace, err := config.CanonicalWorkspace(".")
		if err != nil {
			return err
		}
		cfg, err := config.LoadWorkspaceConfigContext(cmd.Context(), configPath, workspace, false)
		if err != nil {
			return err
		}
		applyFlagOverrides(cmd, cfg)
		entries := cfg.Models()
		if discover {
			entries, err = agent.DiscoverModels(cmd.Context(), cfg, configPath)
			if err != nil {
				return err
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
	}}
	command.Flags().BoolVar(&discover, "discover", false, "Query the active provider for additional models")
	rootCmd.AddCommand(command)
}
