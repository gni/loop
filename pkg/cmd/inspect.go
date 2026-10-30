package cmd

import (
	"encoding/json"
	"github.com/spf13/cobra"
	"loop/pkg/config"
)

func init() {
	configCmd.AddCommand(&cobra.Command{Use: "inspect", Short: "Show effective settings and their sources", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		workspace, err := config.CanonicalWorkspace(".")
		if err != nil {
			return err
		}
		cfg, err := config.LoadWorkspaceConfig(configPath, workspace, config.WorkspaceTrusted(configPath, workspace))
		if err != nil {
			return err
		}
		cfg.ApiKey = redact(cfg.ApiKey)
		for name, profile := range cfg.Providers {
			profile.ApiKey = redact(profile.ApiKey)
			if profile.OAuth != nil {
				profile.OAuth.ClientSecret = redact(profile.OAuth.ClientSecret)
			}
			for key := range profile.Headers {
				profile.Headers[key] = "[configured]"
			}
			cfg.Providers[name] = profile
		}
		for name, server := range cfg.MCPServers {
			for key := range server.Env {
				server.Env[key] = "[configured]"
			}
			for key := range server.Headers {
				server.Headers[key] = "[configured]"
			}
			if server.OAuth != nil {
				server.OAuth.ClientSecret = redact(server.OAuth.ClientSecret)
			}
			cfg.MCPServers[name] = server
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"settings": cfg, "sources": cfg.Sources})
	}})
}

func redact(value string) string {
	if value == "" {
		return ""
	}
	return "[configured]"
}
