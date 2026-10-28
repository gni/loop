package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"loop/pkg/config"
	"loop/pkg/db"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui"
)

var rootCmd = &cobra.Command{
	Use:   "loop [prompt]",
 SilenceUsage:true,SilenceErrors:true,
	Short: "loop is a minimalist, resilient coding agent CLI.",
	Long:  `loop is a Unix-style agent harness and interactive REPL that supports persistent session tracking, tool execution sandboxes, and terminal visual themes.`,
	Args:  cobra.ArbitraryArgs,
	RunE:  runRoot,
}

func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}

func initSessionStore() error {
	sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
	return db.InitDB(sessionsDir)
}

func loadConfigOrPrintErr() *config.Config {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return nil
	}
	if err := applyPromptCatalog(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load prompt catalog: %v\n", err)
	}
	return cfg
}

func saveConfigOrPrintErr(cfg *config.Config) bool {
	if err := config.SaveConfig(configPath, cfg); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return false
	}
	return true
}

func applyPromptCatalog(cfg *config.Config) error {
	path := cfg.PromptsFile
	if path == "" {
		path = domaintool.DefaultPromptCatalogPath()
	}
	return domaintool.LoadPromptCatalog(path)
}

func runRenderCmd(render func(io.Writer, *config.Config, ui.UITheme)) func(cmd *cobra.Command, args []string) {
	return func(cmd *cobra.Command, args []string) {
		cfg := loadConfigOrPrintErr()
		if cfg == nil {
			return
		}
		theme := ui.GetConfiguredTheme(cfg)
		render(os.Stdout, cfg, theme)
	}
}

func isPiped() bool {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) == 0
}

func readStdin() (string, error) {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func init() {
	initRootFlags(rootCmd)
}
