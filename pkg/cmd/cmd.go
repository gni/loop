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
	Short: "loop is a minimalist, resilient AI coding agent CLI.",
	Long:  `loop is a Unix-style agent harness and interactive REPL that supports persistent session tracking, tool execution sandboxes, and advanced terminal visual themes.`,
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

		sessionsDir := filepath.Join(filepath.Dir(configPath), "sessions")
		if err := db.InitDB(sessionsDir); err != nil {
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

		// Instantiate Agent context
		a := agent.NewAgent(cfg, configPath, httpClient)
		a.UI = ui.NewAgentUI(cfg, theme)

		// Load reference skills
		a.ActiveSkills, err = agent.LoadSkills(cfg.SkillsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
		}

		// Start MCP servers
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

		if pipedData != "" || hasPromptArgs {
			var promptBuilder strings.Builder
			if pipedData != "" {
				promptBuilder.WriteString("<stdin>\n")
				promptBuilder.WriteString(pipedData)
				promptBuilder.WriteString("\n</stdin>\n")
			}
			if hasPromptArgs {
				promptBuilder.WriteString(strings.Join(args, " "))
			}

			prompt := promptBuilder.String()
			var messages []db.Message
			if sessionID != "" {
				var err error
				messages, err = db.LoadMessages(sessionID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to load session %s: %v\n", sessionID, err)
				}
			}
			if len(messages) == 0 {
				messages = []db.Message{
					{Role: "system", Content: a.GetSystemPrompt()},
				}
			} else if messages[0].Role == "system" {
				currentSysPrompt := a.GetSystemPrompt()
				if messages[0].Content != currentSysPrompt {
					messages[0].Content = currentSysPrompt
					if sessionID != "" {
						_ = db.RewriteSession(sessionID, messages)
					}
				}
			}

			runCtx := cmd.Context()
			if runCtx == nil {
				runCtx = context.Background()
			}
			a.RunAgentLoop(runCtx, os.Stdout, &messages, prompt, allowedTools, theme, true, sessionID)
			return
		}

		// Interactive REPL Mode
		repl.RunREPL(a, allowedTools, theme, sessionID)
	},
}

// ExecuteContext runs the root command bounded by the provided context.
func ExecuteContext(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
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

// applyPromptCatalog layers the user-editable prompt catalog file over the
// embedded defaults so instructions, rules, and tool descriptions can be
// adjusted without recompiling.
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
