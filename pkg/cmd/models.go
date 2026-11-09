package cmd

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/runtime"
)

func init() {
	var discover bool
	command := &cobra.Command{Use: "models", Short: "List configured model capabilities and prices", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) (runErr error) {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		entries := cfg.Models()
		if discover {
			session, err := runtime.NewContext(cmd.Context(), runtime.Options{Config: cfg, ConfigPath: configPath, NoSession: true, Tools: []string{}})
			if err != nil {
				return err
			}
			defer func() { runErr = errors.Join(runErr, session.Close(context.Background())) }()
			entries, err = session.Agent.DiscoverModels(cmd.Context())
			if err != nil {
				return err
			}
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(entries)
	}}
	command.Flags().BoolVar(&discover, "discover", false, "Query the active provider for additional models")
	rootCmd.AddCommand(command)
}
