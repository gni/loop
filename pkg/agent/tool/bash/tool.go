package bash

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"loop/pkg/domain/evidence"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/process"
	"loop/pkg/ui/style"
)

type bashTool struct {
	gate       chan struct{}
	currentDir string
}

// NewBashTool creates a new bash command executor.
func NewBashTool() domaintool.ToolExecutor {
	return &bashTool{gate: make(chan struct{}, 1)}
}

func (t *bashTool) Name() string { return "bash" }

func (t *bashTool) PromptSnippet() string {
	return domaintool.FormatToolSnippet(t.Name(), "Execute shell commands (builds, tests, git, background processes)")
}

func (t *bashTool) PromptGuidelines() []string {
	return domaintool.FormatToolGuidelines(t.Name(), []string{
		"Working directory persists across commands in this session.",
		"Use 'bash' for build, test, git, and process management. Prefer native file tools ('read', 'edit', 'write') for file operations.",
		"For background processes and long-running services, set 'background': true.",
		"You can inspect LOOP_* environment variables for current model, provider, and workspace details.",
	})
}

func (t *bashTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"bash",
		domaintool.FormatToolDescription("bash", "Execute shell commands inside the workspace (builds, tests, package installation, git commands, and process management). Working directory persists across commands. Captured stdout and stderr are bounded to 8 MiB each. Long output is shortened to fit the configured budget. Use read on the saved output path when shown."),
		map[string]domaintool.SchemaProp{
			"command":    domaintool.StringProp(domaintool.FormatParamDescription("bash", "command", "The command to run in the terminal")),
			"dir":        domaintool.StringProp(domaintool.FormatParamDescription("bash", "dir", "Optional working directory in which to execute the command. Persists across commands for this session.")),
			"background": domaintool.BoolProp(domaintool.FormatParamDescription("bash", "background", "Set to true to run the command in the background as a tracked background task (for servers, daemons, or long-running tasks).")),
			"timeout":    domaintool.NumberProp(domaintool.FormatParamDescription("bash", "timeout", "Optional execution timeout in seconds for this command.")),
		},
		"command",
	)
}

func (t *bashTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	result, err := t.ExecuteResult(ctx, arguments)
	return result.Output, err
}

func (t *bashTool) ExecuteResult(ctx domaintool.AgentContext, arguments string) (domaintool.ToolResult, error) {
	if provider, ok := ctx.(interface {
		DedicatedBashTool() domaintool.ToolExecutor
	}); ok {
		if dedicated := provider.DedicatedBashTool(); dedicated != nil && dedicated != t {
			if rich, ok := dedicated.(domaintool.ResultExecutor); ok {
				return rich.ExecuteResult(ctx, arguments)
			}
			output, err := dedicated.Execute(ctx, arguments)
			return domaintool.ToolResult{Output: output, IsError: err != nil}, err
		}
	}
	observed := &evidence.Record{Kind: "command", Outcome: evidence.NotExecuted}
	output, err := t.execute(ctx, arguments, observed)
	return domaintool.ToolResult{Output: output, IsError: err != nil, Evidence: observed}, err
}

func (t *bashTool) execute(ctx domaintool.AgentContext, arguments string, observed *evidence.Record) (string, error) {
	var args struct {
		Command    string  `json:"command"`
		Cmd        string  `json:"cmd"`
		Arguments  string  `json:"arguments"`
		Dir        string  `json:"dir"`
		Cwd        string  `json:"cwd"`
		Background *bool   `json:"background"`
		Timeout    float64 `json:"timeout"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(s string) { args.Command = s }); err != nil {
		return "", err
	}
	if math.IsNaN(args.Timeout) || math.IsInf(args.Timeout, 0) || args.Timeout < 0 || args.Timeout >= float64(math.MaxInt64)/float64(time.Second) {
		return "", fmt.Errorf("timeout must be a nonnegative number of seconds within the supported duration range")
	}
	args.Command = domaintool.FirstNonEmpty(args.Command, args.Cmd, args.Arguments)
	observed.Target = args.Command
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command parameter is empty")
	}
	if err := ctx.Context().Err(); err != nil {
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}
	gate, ok := ctx.(domaintool.ShellExecutionGate)
	if !ok || !gate.IsShellExecutionAllowed() {
		return "", fmt.Errorf("shell execution requires a trusted workspace; trust it explicitly before running commands")
	}

	args.Dir = domaintool.FirstNonEmpty(args.Dir, args.Cwd)

	select {
	case t.gate <- struct{}{}:
		defer func() { <-t.gate }()
	case <-ctx.Context().Done():
		return "", fmt.Errorf("command cancelled before execution: %w", ctx.Context().Err())
	}
	if err := ctx.Context().Err(); err != nil {
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}

	persistentBash := true
	if cp, ok := ctx.(domaintool.PersistentShellSettings); ok {
		persistentBash = cp.IsPersistentBashEnabled()
	}
	if !persistentBash {
		t.currentDir = ""
	}

	workspaceRoot := ctx.GetWorkspaceRoot()
	workDir := t.currentDir
	if workDir == "" {
		workDir = workspaceRoot
	}

	if args.Dir != "" {
		safeDir, err := ctx.SafePath(args.Dir)
		if err != nil {
			return "", fmt.Errorf("invalid working directory '%s': %w", args.Dir, err)
		}
		if info, err := os.Stat(safeDir); err != nil || !info.IsDir() {
			return "", fmt.Errorf("working directory '%s' does not exist or is not a directory", args.Dir)
		}
		workDir = safeDir
	} else {
		safeDir, safeErr := ctx.SafePath(workDir)
		if info, err := os.Stat(safeDir); safeErr != nil || err != nil || !info.IsDir() {
			workDir = workspaceRoot
			t.currentDir = workspaceRoot
		} else {
			workDir = safeDir
		}
	}

	normalized, isBgCmd := backgroundCommand(args.Command)
	if args.Background != nil {
		isBgCmd = *args.Background
	}

	if isBgCmd {
		var id string
		var err error
		bgCmd := normalized
		if spawner, ok := ctx.(domaintool.BackgroundTaskSpawner); ok {
			id, err = spawner.SpawnTaskWithOptions(ctx.Context(), bgCmd, os.Stderr, domaintool.BackgroundTaskOptions{Directory: workDir})
		} else {
			if workDir != workspaceRoot {
				bgCmd = "cd -- " + ShellQuote(workDir) + " && {\n" + bgCmd + "\n}"
			}
			id, err = ctx.SpawnTask(bgCmd, os.Stderr)
		}
		if err != nil {
			return "", fmt.Errorf(domaintool.RuntimeMessage("task_spawn_failed", "failed to spawn background task: %w"), err)
		}
		if persistentBash {
			t.currentDir = workDir
		}
		observed.Outcome, observed.TaskID = evidence.Started, id
		return domaintool.RuntimeMessagef("task_spawned", "Task spawned in background with ID: %s (command: %s). Monitor output with /status (or 'get_task_status') or stop with /stop (or 'stop_task'). Toggle live stream via Ctrl+O.", id, bgCmd), nil
	}

	timeout := shellTimeout(ctx)
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout * float64(time.Second))
	}
	timeoutCtx, cancel := context.WithTimeout(ctx.Context(), timeout)
	defer cancel()

	wrappedCmd := args.Command
	var directory *directoryCapture
	if persistentBash {
		var err error
		wrappedCmd, directory, err = captureDirectory(args.Command)
		if err != nil {
			return "", err
		}
		defer directory.Close()
	}
	if settings, ok := ctx.(domaintool.ShellScanSettings); ok && settings.IsShellScanDefaultsEnabled() {
		wrappedCmd = WithScanDefaults(wrappedCmd, workspaceRoot)
	}
	cmd := NewCommand(wrappedCmd)
	cmd.Dir = workDir
	if ep, ok := ctx.(domaintool.ExecutionEnvironment); ok {
		for k, v := range ep.GetExecutionEnv() {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &process.Buffer{Limit: 8 << 20}
	stderr := &process.Buffer{Limit: 8 << 20}
	outWriter := io.Writer(stdout)
	errWriter := io.Writer(stderr)
	live := newLiveShellOutput(ctx)
	var finishStdout func()
	if live != nil {
		liveOut, liveErr, finish := live.Streams()
		outWriter = io.MultiWriter(stdout, bestEffortLiveWriter{liveOut})
		errWriter = io.MultiWriter(stderr, bestEffortLiveWriter{liveErr})
		finishStdout = finish
	}
	if err := timeoutCtx.Err(); err != nil {
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}
	wait, err := process.StartCapture(cmd, outWriter, errWriter)
	if err != nil {
		return "", fmt.Errorf("failed to start command: %w", err)
	}
	if persistentBash {
		t.currentDir = workDir
	}
	observed.Outcome = evidence.Unknown
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case <-timeoutCtx.Done():
		_ = process.KillProcessGroup(cmd)
		err = <-done
	case err = <-done:
		if cmd.ProcessState != nil {
			if code := cmd.ProcessState.ExitCode(); code >= 0 {
				observed.ExitCode = &code
			}
			observed.Outcome = evidence.Succeeded
			if err != nil || !cmd.ProcessState.Success() {
				observed.Outcome = evidence.Failed
			}
		}
	}
	if timeoutCtx.Err() != nil {
		err = timeoutCtx.Err()
	}
	if finishStdout != nil {
		finishStdout()
		live.Finish()
	}

	if timeoutCtx.Err() == context.Canceled {
		return "", fmt.Errorf("command cancelled by user: %w", context.Canceled)
	}

	output := cleanShellOutput(stdout.String())
	errOutput := cleanShellOutput(stderr.String())
	if stdout.Overflow {
		output += "\n[stdout capture limit reached at 8 MiB; later bytes discarded]\n"
	}
	if stderr.Overflow {
		errOutput += "\n[stderr capture limit reached at 8 MiB; later bytes discarded]\n"
	}

	if directory != nil && timeoutCtx.Err() == nil {
		if extractedDir := directory.Read(ctx.Context()); extractedDir != "" {
			if safe, err := ctx.SafePath(extractedDir); err == nil {
				if info, err := os.Stat(safe); err == nil && info.IsDir() {
					t.currentDir = safe
				}
			}
		}
	}

	combined := output
	if errOutput != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += errOutput
	}

	if err != nil {
		exitDesc := FormatExitErrorWithTimeout(err, timeoutCtx, timeout)
		if strings.TrimSpace(combined) == "" {
			combined = fmt.Sprintf("%s (no output on stdout or stderr)", exitDesc)
		} else if !strings.Contains(combined, "exit status") && !strings.Contains(combined, "exit code") {
			combined = fmt.Sprintf("%s\n\n(%s)", strings.TrimRight(combined, "\r\n"), exitDesc)
		}
		if timeoutCtx.Err() != nil {
			return combined, fmt.Errorf("%s: %w", exitDesc, timeoutCtx.Err())
		}
		return combined, fmt.Errorf("%s", exitDesc)
	}
	return combined, nil
}

func shellTimeout(ctx domaintool.AgentContext) time.Duration {
	if provider, ok := ctx.(domaintool.ShellTimeoutSettings); ok {
		return provider.GetShellTimeout()
	}
	return 120 * time.Second
}

func cleanShellOutput(raw string) string {
	valid := strings.ToValidUTF8(raw, "\uFFFD")
	stripped := style.StripAnsi(valid)
	if !strings.Contains(stripped, "\r") {
		return stripped
	}
	stripped = strings.ReplaceAll(stripped, "\r\n", "\n")
	lines := strings.Split(stripped, "\n")
	for i, line := range lines {
		if strings.Contains(line, "\r") {
			parts := strings.Split(line, "\r")
			for j := len(parts) - 1; j >= 0; j-- {
				trimmed := strings.TrimSpace(parts[j])
				if trimmed != "" {
					lines[i] = parts[j]
					break
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}
