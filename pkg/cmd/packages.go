package cmd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"loop/pkg/packages"
)

func init() {
	descriptions := map[string]string{
		"install": "Install an extension, skill, or plugin package",
		"remove":  "Remove an installed package",
		"update":  "Update installed packages",
		"list":    "List installed packages",
	}
	for _, operation := range []string{"install", "remove", "update", "list"} {
		var local bool
		command := &cobra.Command{
			Use:   operation + " [source]",
			Short: descriptions[operation],
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				directory := filepath.Dir(configPath)
				if local {
					directory = ".loop"
				}
				manager := packages.Manager{Directory: directory}
				source := ""
				if len(args) > 0 {
					source = args[0]
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
				defer cancel()
				switch cmd.Name() {
				case "list":
					values, err := manager.List()
					if err != nil {
						return err
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(values)
				case "install":
					installed, err := manager.Install(ctx, source)
					if err != nil {
						return err
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(installed)
				case "remove":
					return manager.Remove(source)
				default:
					return manager.Update(ctx, source)
				}
			}}
		command.Flags().BoolVar(&local, "local", false, "Use the project package registry")
		rootCmd.AddCommand(command)
	}
}
