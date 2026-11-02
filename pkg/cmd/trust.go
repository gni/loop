package cmd

import (
	"fmt"
	"os"
	"strings"

	"loop/pkg/config"
)

func promptWorkspaceTrust(cwd string) bool {
	if config.WorkspaceTrusted(configPath, cwd) {
		return true
	}
	fmt.Printf("\n\x1b[33m⚠️  Workspace Trust\x1b[0m: %s\n", cwd)
	fmt.Print("Shell commands and local executable integrations can access your machine. Trust this workspace? [y/N]: ")
	var resp string
	_, _ = fmt.Scanln(&resp)
	resp = strings.ToLower(strings.TrimSpace(resp))
	if resp == "y" || resp == "yes" {
		if err := config.TrustWorkspace(configPath, cwd, true); err != nil {
			fmt.Fprintf(os.Stderr, "cannot save workspace trust: %v\n", err)
			fmt.Println("Untrusted workspace: shell execution and project plugins are unavailable.")
			return false
		}
		fmt.Println("\x1b[32mWorkspace trusted.\x1b[0m")
		return true
	}
	fmt.Println("Untrusted workspace: shell execution and project plugins are unavailable.")
	return false
}
