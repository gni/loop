package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"loop/pkg/auth"
	"loop/pkg/config"
)

func credentialCommand(mcp bool) *cobra.Command {
	name := "auth"
	if mcp {
		name = "credentials"
	}
	command := &cobra.Command{Use: name, Short: "Manage private API credentials and OAuth sign-in"}
	var environment string
	var device bool
	resolve := func(cmd *cobra.Command, args []string) (*auth.Store, string, config.ProviderConfig, error) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			return nil, "", config.ProviderConfig{}, err
		}
		store := auth.NewStore(filepath.Join(filepath.Dir(configPath), "auth.json"))
		key := args[0]
		if mcp {
			server, exists := cfg.MCPServers[key]
			if !exists || server.URL == "" {
				return nil, "", config.ProviderConfig{}, fmt.Errorf("HTTP MCP server %q does not exist", key)
			}
			profile := config.ProviderConfig{Endpoint: server.URL, OAuth: server.OAuth}
			if cmd.Name() == "login" && profile.OAuth == nil {
				profile.OAuth, err = store.Discover(cmd.Context(), server.URL)
				if err != nil {
					return nil, "", profile, err
				}
				server.OAuth = profile.OAuth
				cfg.MCPServers[key] = server
				if err := config.SaveConfig(configPath, cfg); err != nil {
					return nil, "", profile, err
				}
			}
			return store, "mcp:" + key + ":" + config.WorkspaceKey(server.URL), profile, nil
		}
		profile, exists := cfg.Providers[key]
		if !exists {
			return nil, "", profile, fmt.Errorf("provider %q does not exist", key)
		}
		return store, key, profile, nil
	}
	login := &cobra.Command{Use: "login <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, key, profile, err := resolve(cmd, args)
		if err != nil {
			return err
		}
		if environment != "" {
			if os.Getenv(environment) == "" {
				return fmt.Errorf("environment variable %s is empty", environment)
			}
			return store.Save(key, auth.Credential{Type: "api_key", Issuer: profile.Endpoint, Environment: environment})
		}
		return store.Login(cmd.Context(), key, profile, device, func(message string) { fmt.Fprintln(cmd.ErrOrStderr(), message) })
	}}
	login.Flags().StringVar(&environment, "env", "", "Read the API key from this environment variable")
	login.Flags().BoolVar(&device, "device", false, "Use the configured device authorization flow")
	logout := &cobra.Command{Use: "logout <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, key, _, err := resolve(cmd, args)
		if err != nil {
			return err
		}
		return store.Logout(key)
	}}
	check := &cobra.Command{Use: "check <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		store, key, profile, err := resolve(cmd, args)
		if err != nil {
			return err
		}
		token, err := store.Resolve(cmd.Context(), key, profile)
		if err != nil {
			return err
		}
		if token == "" {
			return fmt.Errorf("credentials are not configured")
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), "ready")
		return err
	}}
	command.AddCommand(login, logout, check)
	return command
}

func init() {
	rootCmd.AddCommand(credentialCommand(false))
	commands := credentialCommand(true)
	for _, command := range commands.Commands() {
		mcpCmd.AddCommand(command)
	}
}
