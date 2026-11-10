package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/resources"
	"loop/pkg/runtime"
	"loop/pkg/ui"
	"loop/pkg/ui/repl"
)

var outputMode string
var printOnce bool
var noSession bool
var noTools bool
var trustProject bool
var ignoreProject bool
var sessionName string

func runRoot(cmd *cobra.Command, args []string) (runErr error) {
	if outputMode != "text" && outputMode != "json" && outputMode != "rpc" {
		return fmt.Errorf("invalid output mode %q", outputMode)
	}
	workspace, err := config.CanonicalWorkspace(".")
	if err != nil {
		return err
	}
	noninteractive := printOnce || len(args) > 0 || outputMode != "text" || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))
	trusted := config.WorkspaceTrusted(configPath, workspace)
	if cmd.Flags().Changed("trust") {
		trusted = trustProject
	} else if ignoreProject {
		trusted = false
	} else if !noninteractive {
		trusted = promptWorkspaceTrust(workspace)
	}
	cfg, err := config.LoadWorkspaceConfigContext(cmd.Context(), configPath, workspace, trusted)
	if err != nil {
		return err
	}
	if err := applyPromptCatalog(cfg); err != nil {
		return err
	}
	applyFlagOverrides(cmd, cfg)
	if cmd.Flags().Changed("nostop-interval") && !cmd.Flags().Changed("nostop") {
		return fmt.Errorf("--nostop-interval requires --nostop <mode>")
	}
	if cmd.Flags().Changed("nostop") && strings.TrimSpace(nostopModeFlag) == "" {
		return fmt.Errorf("--nostop requires eco, balanced, continuous, or off")
	}
	if err := cfg.ValidateSettings(); err != nil {
		return err
	}
	allowed := parseAllowedTools(allowedToolsStr)
	if cmd.Flags().Changed("tools") && strings.TrimSpace(allowedToolsStr) != "" && len(allowed) == 0 && !strings.EqualFold(strings.TrimSpace(allowedToolsStr), "none") && !strings.EqualFold(strings.TrimSpace(allowedToolsStr), "empty") {
		return fmt.Errorf("--tools must contain tool names or none")
	}
	if noTools {
		allowed = []string{}
	}
	options := runtime.Options{Config: cfg, ConfigPath: configPath, Workspace: workspace, SessionID: sessionIDFlag, SessionName: sessionName, Resume: resumeSession, NoSession: noSession, Tools: allowed, KeepSelection: cmd.Flags().Changed("model") || cmd.Flags().Changed("endpoint") || cmd.Flags().Changed("reasoning")}
	if outputMode == "text" && cfg.Nostop.Enabled() {
		options.Output = cmd.OutOrStdout()
	}
	var outputMu sync.Mutex
	encode := func(value any) error {
		outputMu.Lock()
		defer outputMu.Unlock()
		return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
	}
	if outputMode == "json" || outputMode == "rpc" {
		options.Events = func(event agent.Event) error { return encode(event) }
	}
	if outputMode == "rpc" {
		if len(args) > 0 {
			return fmt.Errorf("RPC mode accepts prompts through stdin commands")
		}
		return serveRPC(cmd.Context(), options, cmd.InOrStdin(), encode)
	}
	s, err := runtime.NewContext(cmd.Context(), options)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, s.Close(context.Background())) }()
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	if !noninteractive {
		if s.Store != nil {
			// The terminal's paged history adapter currently consumes JSONL files.
			store, ok := s.Store.(*db.JSONLStore)
			if !ok {
				return fmt.Errorf("interactive history requires JSONL session storage")
			}
			db.SetDefaultStore(store)
		} else if err := initSessionStore(); err != nil {
			return err
		}
		theme := ui.GetConfiguredTheme(cfg)
		s.Agent.UI = ui.NewAgentUI(s.Agent.Config, theme)
		repl.RunREPLContext(cmd.Context(), s.Agent, allowed, theme, s.ID)
		return nil
	}
	var pipedInput string
	hasPipedStdin := false
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		input, stopInput := cancellableInput(cmd.Context(), cmd.InOrStdin())
		defer stopInput()
		data, err := io.ReadAll(io.LimitReader(input, (16<<20)+1))
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		if len(data) > 16<<20 {
			return fmt.Errorf("stdin exceeds 16 MiB")
		}
		pipedInput = string(data)
		hasPipedStdin = true
	}
	explicitArgs := strings.Join(args, " ")
	input := pipedInput
	if explicitArgs != "" {
		if input != "" {
			input += "\n\n"
		}
		input += explicitArgs
	}
	if strings.TrimSpace(input) == "" {
		return fmt.Errorf("provide a prompt or pipe input")
	}
	runCtx := cmd.Context()
	if hasPipedStdin {
		runCtx = resources.WithAttachmentSource(runCtx, input, explicitArgs)
	}
	result, err := s.Run(runCtx, input)
	if err != nil {
		return err
	}
	if outputMode == "text" && !cfg.Nostop.Enabled() {
		_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Text)
	}
	return err
}
