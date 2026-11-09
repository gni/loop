package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/ui/render"
)

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "Manage endpoint provider profiles",
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all configured endpoint providers",
	RunE:  runRenderCmd(render.RenderProviders),
}

var providerAddCmd = &cobra.Command{
	Use:   "add <name> <url> [key] [model]",
	Short: "Add a new endpoint provider profile",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		name := args[0]
		url := args[1]
		var key, model string
		if len(args) > 2 {
			key = args[2]
		}
		if len(args) > 3 {
			model = strings.Join(args[3:], " ")
		}

		if cfg.Providers == nil {
			cfg.Providers = make(map[string]config.ProviderConfig)
		}
		cfg.Providers[name] = config.ProviderConfig{
			Name:     name,
			Endpoint: url,
			APIKey:   key,
			Model:    model,
		}
		if cfg.ActiveProvider == name {
			if err := cfg.ActivateProvider(name); err != nil {
				return err
			}
		}
		if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Provider '%s' successfully added/updated.\n", name)
		return nil
	},
}

var providerSelectCmd = &cobra.Command{
	Use:   "select <name>",
	Short: "Select active endpoint provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		name := args[0]
		if name == "none" || name == "default" {
			cfg.ActiveProvider = ""
			if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
				return err
			}
			fmt.Println("Switched active provider to default settings.")
			return nil
		}

		if _, ok := cfg.Providers[name]; !ok {
			return fmt.Errorf("provider %q not found", name)
		}

		if err := cfg.ActivateProvider(name); err != nil {
			return err
		}
		if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Switched active provider to '%s'.\n", name)
		return nil
	},
}

var providerModelCmd = &cobra.Command{
	Use:   "model <model>",
	Short: "Set model for the currently active endpoint provider",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		model := strings.Join(args, " ")
		cfg.Model = model
		cfg.UpdateActiveProvider()
		if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
			return err
		}
		if cfg.ActiveProvider != "" {
			fmt.Printf("Updated model for provider '%s' to '%s'.\n", cfg.ActiveProvider, model)
		} else {
			fmt.Printf("Updated default model to '%s'.\n", model)
		}
		return nil
	},
}

var providerRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an endpoint provider profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		name := args[0]
		if _, ok := cfg.Providers[name]; !ok {
			return fmt.Errorf("provider %q not found", name)
		}

		delete(cfg.Providers, name)
		if cfg.ActiveProvider == name {
			cfg.ActiveProvider = ""
		}
		if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Provider '%s' removed.\n", name)
		return nil
	},
}

var providerTimeoutCmd = &cobra.Command{
	Use:   "timeout <seconds>",
	Short: "Set timeout in seconds for the currently active endpoint provider",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.LoadConfigContext(cmd.Context(), configPath)
		if err != nil {
			return err
		}
		sec, err := strconv.Atoi(args[0])
		if err != nil || sec < 0 {
			return fmt.Errorf("timeout must be a non-negative integer")
		}
		cfg.Timeout = sec
		cfg.UpdateActiveProvider()
		if err := config.SaveConfigContext(cmd.Context(), configPath, cfg); err != nil {
			return err
		}
		if cfg.ActiveProvider != "" {
			fmt.Printf("Updated timeout for provider '%s' to %ds.\n", cfg.ActiveProvider, sec)
		} else {
			fmt.Printf("Updated default timeout to %ds.\n", sec)
		}
		return nil
	},
}

func init() {
	providerCmd.AddCommand(providerListCmd, providerAddCmd, providerSelectCmd, providerModelCmd, providerTimeoutCmd, providerRemoveCmd)
	rootCmd.AddCommand(providerCmd)
}
