package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"loop/pkg/config"
)

func ensureWorkspaceTrust(cwd string, cfg *config.Config, isNonInteractive bool) {
	trustFile := filepath.Join(cwd, ".loop-trust")
	trusted := false
	if _, err := os.Stat(trustFile); err == nil {
		trusted = true
	}

	hasLocalCode := dirHasEntries(filepath.Join(cwd, "plugins")) || dirHasEntries(filepath.Join(cwd, "extensions"))

	if hasLocalCode && !trusted && !isNonInteractive {
		fmt.Printf("\n\x1b[33m⚠️  Security Warning\x1b[0m: Workspace '%s' contains local plugins or extensions.\n", cwd)
		fmt.Printf("These can execute arbitrary code on your machine. Do you trust this workspace? [y/N]: ")
		var resp string
		fmt.Scanln(&resp)
		resp = strings.ToLower(strings.TrimSpace(resp))
		if resp == "y" || resp == "yes" {
			trusted = true
			_ = os.WriteFile(trustFile, []byte("trusted"), 0644)
			fmt.Println("\x1b[32mWorkspace trusted.\x1b[0m")
		}
	}

	if hasLocalCode && !trusted {
		if !isNonInteractive {
			fmt.Println("\x1b[31mLocal plugins and extensions have been DISABLED for this session.\x1b[0m")
		}
		cfg.DisableLocalPlugins = true
	}
}

func dirHasEntries(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

