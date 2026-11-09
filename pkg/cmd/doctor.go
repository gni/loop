package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"loop/pkg/config"
	"loop/pkg/diagnostics"
)

func init() {
	offline := false
	command := &cobra.Command{Use: "doctor", Short: "Check configuration, endpoint, authentication and local tools", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		workspace, err := config.CanonicalWorkspace(".")
		if err != nil {
			return err
		}
		cfg, err := config.LoadWorkspaceConfigContext(cmd.Context(), configPath, workspace, config.WorkspaceTrusted(configPath, workspace))
		if err != nil {
			return err
		}
		applyFlagOverrides(cmd, cfg)
		report := diagnostics.Run(cmd.Context(), cfg, configPath, workspace, offline)
		report.Write(cmd.OutOrStdout())
		if report.Failed() {
			return fmt.Errorf("doctor found issues that need attention")
		}
		return nil
	}}
	command.Flags().BoolVar(&offline, "offline", false, "Check local settings without contacting the provider")
	rootCmd.AddCommand(command)
}
