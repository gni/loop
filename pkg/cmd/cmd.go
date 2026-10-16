package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	domaintool "loop/pkg/domain/tool"
	transporthttp "loop/pkg/transport/http"
	"loop/pkg/ui"
	"loop/pkg/ui/repl"
)

var rootCmd = &cobra.Command{
	Use:   "loop [prompt]",
	Short: "loop is a minimalist, resilient coding agent CLI.",
	Long:  `loop is a Unix-style agent harness and interactive REPL that supports persistent session tracking, tool execution sandboxes, and terminal visual themes.`,
	Args:  cobra.ArbitraryArgs,
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig(configPath)
		if err != nil {
			fmt.Printf("Error loading configuration: %v\n", err)
			os.Exit(1)
		}

		if err := applyPromptCatalog(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load prompt catalog: %v\n", err)
		}

		if err := initSessionStore(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to initialize session storage: %v\n", err)
		}

		applyFlagOverrides(cmd, cfg)
		theme := ui.GetConfiguredTheme(cfg)
		allowedTools := parseAllowedTools(allowedToolsStr)

		tlsConfig, err := config.GetTLSConfig(cfg)
		if err != nil {
			fmt.Printf("Warning: Failed to setup SSL certificates: %v. Proceeding without client certificates.\n", err)
			tlsConfig = nil
		}

		httpClient := transporthttp.NewHTTPClient(tlsConfig, cfg.Timeout)
		cwd, _ := os.Getwd()

		pipedData := ""
		if isPiped() {
			pipedData, _ = readStdin()
		}
		hasPromptArgs := len(args) > 0
		isNonInteractive := pipedData != "" || hasPromptArgs

		ensureWorkspaceTrust(cwd, cfg, isNonInteractive)

		a := agent.NewAgent(cfg, configPath, httpClient)
		a.UI = ui.NewAgentUI(cfg, theme)

		a.ActiveSkills, err = agent.LoadSkills(cfg.SkillsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
		}

		if len(cfg.MCPServers) > 0 {
			_ = a.StartMCPServers(cfg.MCPServers)
			defer a.StopMCPServers()

			if len(a.McpStartErrors) > 0 && isNonInteractive {
				ui.RenderMCPStartupErrors(os.Stderr, a.McpStartErrors, theme)
			}
		}

		sessionID := sessionIDFlag
		if sessionID == "" {
			if resumeSession {
				if lastID, err := db.GetLatestSessionID(); err == nil && lastID != "" {
					sessionID = lastID
				}
			}
			if sessionID == "" {
				sessionID = db.NewUUID()
			}
		}

		if isNonInteractive {
			var promptBuilder strings.Builder
			if pipedData != "" {
				promptBuilder.WriteString("<stdin>\n")
				promptBuilder.WriteString(pipedData)
				promptBuilder.WriteString("\n</stdin>\n")
			}
			if hasPromptArgs {
				promptBuilder.WriteString(strings.Join(args, " "))
			}

			var messages []db.Message
			if sessionID != "" {
				messages, err = db.LoadMessages(sessionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to load session %s: %v\n", sessionID, err)
				}
			}
			currentSysPrompt := a.GetSystemPrompt()
			if len(messages) == 0 {
				messages = []db.Message{
					{Role: "system", Content: currentSysPrompt},
				}
			} else if messages[0].Role == "system" && messages[0].Content != currentSysPrompt {
				messages[0].Content = currentSysPrompt
				if sessionID != "" {
					_ = db.RewriteSession(sessionID, messages)
				}
			}

			runCtx := cmd.Context()
			if runCtx == nil {
				runCtx = context.Background()
			}
			a.RunAgentLoop(runCtx, os.Stdout, &messages, promptBuilder.String(), allowedTools, theme, true, sessionID)
			return
		}

		repl.RunREPL(a, allowedTools, theme, sessionID)
	},
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