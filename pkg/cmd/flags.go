package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"loop/pkg/config"
)

var (
	configPath              string
	endpoint                string
	modelName               string
	autoYes                 bool
	showThinking            bool
	showTokens              bool
	allowedToolsStr         string
	sessionIDFlag           string
	maxStepsFlag            int
	resumeSession           bool
	reasoningEffortFlag     string
	contextLimitFlag        int
	directCommandsFlag      bool
	maxCompletionTokensFlag int
	compactPrompt           bool
	debugFileFlag           string
	maxSubagentDepthFlag    int
	timeoutFlag             int
	recapIntervalFlag       int
)

func applyFlagOverrides(cmd *cobra.Command, cfg *config.Config) {
	if endpoint != "" {
		cfg.Endpoint = endpoint
	}
	if modelName != "" {
		cfg.Model = modelName
	}
	if autoYes {
		cfg.AutoApprove = true
	}
	if showThinking {
		cfg.ShowThinking = true
	}
	if showTokens {
		cfg.ShowTokens = true
	}
	if maxStepsFlag != 0 {
		cfg.MaxReasoningSteps = maxStepsFlag
	}
	if cmd.Flags().Changed("recap") {
		cfg.RecapInterval = recapIntervalFlag
	}
	if reasoningEffortFlag != "" {
		cfg.ReasoningEffort = reasoningEffortFlag
	}
	if contextLimitFlag != 0 {
		cfg.ContextWindowLimit = contextLimitFlag
	}
	if cmd.Flags().Changed("direct") {
		cfg.DirectCommands = directCommandsFlag
	}
	if maxCompletionTokensFlag != 0 {
		cfg.MaxCompletionTokens = maxCompletionTokensFlag
	}
	if cmd.Flags().Changed("compact") {
		cfg.CompactPrompt = compactPrompt
	}
	if debugFileFlag != "" {
		cfg.DebugLogFile = debugFileFlag
	}
	if cmd.Flags().Changed("max-subagent-depth") {
		cfg.MaxSubagentDepth = maxSubagentDepthFlag
	}
	if cmd.Flags().Changed("timeout") {
		cfg.Timeout = timeoutFlag
	}
}

func parseAllowedTools(toolsStr string) []string {
	if toolsStr == "" {
		return nil
	}
	parts := strings.Split(toolsStr, ",")
	var result []string
	for _, t := range parts {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func initRootFlags(cmd *cobra.Command) {
	defaultConfig := "config/config.json"
	home, err := os.UserHomeDir()
	if err == nil {
		defaultConfig = filepath.Join(home, ".loop", "config.json")
	}
	cmd.SetHelpCommand(&cobra.Command{Use: "no-help-command", Hidden: true})
	cmd.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "Path to config JSON file")
	cmd.PersistentFlags().StringVar(&endpoint, "endpoint", "", "Override llama.cpp or OpenAI server URL")
	cmd.PersistentFlags().StringVar(&modelName, "model", "", "Override model name")
	cmd.PersistentFlags().BoolVarP(&autoYes, "yes", "y", false, "Auto-approve all tool execution prompts without asking")
	cmd.PersistentFlags().BoolVar(&showThinking, "thinking", false, "Show streaming LLM thinking/reasoning process")
	cmd.PersistentFlags().BoolVarP(&showTokens, "tokens", "t", false, "Show token usage metrics under each LLM response")
	cmd.PersistentFlags().StringVar(&allowedToolsStr, "tools", "", "Comma-separated list of allowed tools (leave empty for all)")
	cmd.PersistentFlags().StringVarP(&sessionIDFlag, "session", "s", "", "Resume a specific persistent conversation session ID")
	cmd.PersistentFlags().BoolVarP(&resumeSession, "resume", "r", false, "Resume the latest conversation session instead of starting a new one")
	cmd.PersistentFlags().StringVar(&reasoningEffortFlag, "reasoning", "", "Override LLM reasoning effort (e.g. low, medium, high)")
	cmd.PersistentFlags().IntVar(&maxStepsFlag, "steps", 0, "Override maximum reasoning steps limit (e.g. 30)")
	cmd.PersistentFlags().IntVar(&contextLimitFlag, "context-limit", 0, "Override context window limit (default: 128000)")
	cmd.PersistentFlags().IntVar(&maxCompletionTokensFlag, "max-completion-tokens", 0, "Override maximum completion/output tokens limit (default: 16384)")
	cmd.PersistentFlags().BoolVar(&directCommandsFlag, "direct", false, "Enable direct execution of local shell commands (default: true in config)")
	cmd.PersistentFlags().BoolVar(&compactPrompt, "compact", false, "Enable highly compressed system instructions for smaller models")
	cmd.PersistentFlags().StringVar(&debugFileFlag, "debug-file", "", "Path to debug execution log file (default: loop_debug.log in workspace)")
	cmd.PersistentFlags().IntVar(&maxSubagentDepthFlag, "max-subagent-depth", 0, "Maximum subagent nesting depth (default: 0, leaf subagents cannot spawn further subagents)")
	cmd.PersistentFlags().IntVar(&timeoutFlag, "timeout", 0, "Override LLM request timeout in seconds (default: 120)")
	cmd.PersistentFlags().IntVar(&recapIntervalFlag, "recap", 5, "Inject a turn/token recap into the agent history every N reasoning steps (negative disables)")
}
