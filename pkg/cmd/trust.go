package cmd

import (
	"fmt"
	"os"
	"strings"

	"loop/pkg/config"
)

func ensureWorkspaceTrust(cwd string, cfg *config.Config, isNonInteractive bool) {
	trusted := config.WorkspaceTrusted(configPath, cwd)

	cfg.DisableProjectPlugins = !trusted
	if !trusted && !isNonInteractive {
		fmt.Printf("\n\x1b[33m⚠️  Workspace Trust\x1b[0m: %s\n", cwd)
		fmt.Print("Shell commands and local executable integrations can access your machine. Trust this workspace? [y/N]: ")
		var resp string
		fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp == "y" || resp == "yes" {
			if err := config.TrustWorkspace(configPath, cwd, true); err != nil {
				fmt.Fprintf(os.Stderr, "cannot save workspace trust: %v\n", err)
			} else {
				trusted = true
				cfg.DisableProjectPlugins = false
				fmt.Println("\x1b[32mWorkspace trusted.\x1b[0m")
			}
		}
	}

	if !trusted {
		if !isNonInteractive {
			fmt.Println("Untrusted workspace: shell execution and project plugins are unavailable.")
		}
		cfg.DisableProjectPlugins = true
	}
}
