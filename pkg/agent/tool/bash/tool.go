package bash

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"loop/pkg/domain/evidence"
	domaintool "loop/pkg/domain/tool"
	"loop/pkg/ui/style"
)

type bashTool struct {
	mu         sync.Mutex
	currentDir string
}

// NewBashTool creates a new bash command executor.
func NewBashTool() domaintool.ToolExecutor {
	return &bashTool{}
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
		domaintool.FormatToolDescription("bash", "Execute shell commands inside the workspace (builds, tests, package installation, git commands, and process management). Working directory persists across commands. Captured stdout and stderr are bounded to 8 MiB each. Returned output is pruned to 25 head and 25 tail lines or 8192 bytes, with retained output spilled to .loop/spill/."),
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
		Background bool    `json:"background"`
		Timeout    float64 `json:"timeout"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(s string) { args.Command = s }); err != nil {
		return "", err
	}
	args.Command = domaintool.FirstNonEmpty(args.Command, args.Cmd, args.Arguments)
	observed.Target = args.Command
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command parameter is empty")
	}
	if err := ctx.Context().Err(); err != nil {
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}
	gate, ok := ctx.(interface{ IsShellExecutionAllowed() bool })
	if !ok || !gate.IsShellExecutionAllowed() {
		return "", fmt.Errorf("shell execution requires a trusted workspace; trust it explicitly before running commands")
	}

	args.Dir = domaintool.FirstNonEmpty(args.Dir, args.Cwd)

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Context().Err(); err != nil {
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}

	persistentBash := true
	if cp, ok := ctx.(interface{ IsPersistentBashEnabled() bool }); ok {
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
		t.currentDir = safeDir
	} else {
		if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
			workDir = workspaceRoot
			t.currentDir = workspaceRoot
		}
	}

	isBgCmd := args.Background
	trimmedCmd := strings.TrimSpace(args.Command)
	if !isBgCmd {
		if strings.HasPrefix(trimmedCmd, "nohup ") ||
			strings.HasSuffix(trimmedCmd, "&") ||
			strings.Contains(trimmedCmd, "& echo $!") ||
			strings.Contains(trimmedCmd, "& echo $") {
			isBgCmd = true
			args.Command = CleanBackgroundCommand(args.Command)
		}
	}

	if isBgCmd {
		bgCmd := args.Command
		if workDir != workspaceRoot {
			bgCmd = "cd " + ShellQuote(workDir) + " && " + bgCmd
		}
		id, err := ctx.SpawnTask(bgCmd, os.Stderr)
		if err != nil {
			return "", fmt.Errorf(domaintool.RuntimeMessage("task_spawn_failed", "failed to spawn background task: %w"), err)
		}
		observed.Outcome, observed.TaskID = evidence.Started, id
		return domaintool.RuntimeMessagef("task_spawned", "Task spawned in background with ID: %s (command: %s). Monitor output with /status (or 'task_status') or stop with /stop (or 'task_kill'). Toggle live stream via Ctrl+O.", id, bgCmd), nil
	}

	timeout := shellTimeout(ctx)
	if args.Timeout > 0 {
		timeout = time.Duration(args.Timeout * float64(time.Second))
	}
	timeoutCtx, cancel := context.WithTimeout(ctx.Context(), timeout)
	defer cancel()

	var markerBytes [16]byte
	if _, err := rand.Read(markerBytes[:]); err != nil {
		return "", fmt.Errorf("cannot generate command boundary: %w", err)
	}
	pwdMarker := "__LOOP_PWD_" + hex.EncodeToString(markerBytes[:]) + ":"
	wrappedCmd := fmt.Sprintf("__loop_exit() {\n  __loop_ret=$?\n  printf '\\n%%s%%s\\n' %s \"$(pwd -P)\"\n  exit $__loop_ret\n}\ntrap __loop_exit EXIT\n%s", ShellQuote(pwdMarker), args.Command)
	if settings, ok := ctx.(interface{ IsShellScanDefaultsEnabled() bool }); ok && settings.IsShellScanDefaultsEnabled() {
		wrappedCmd = WithScanDefaults(wrappedCmd, workspaceRoot)
	}
	cmd := NewCommand(wrappedCmd)
	cmd.Dir = workDir
	if ep, ok := ctx.(interface{ GetExecutionEnv() map[string]string }); ok {
		for k, v := range ep.GetExecutionEnv() {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		return "", fmt.Errorf("failed to create stderr pipe: %w", err)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	if err := timeoutCtx.Err(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return "", fmt.Errorf("command cancelled before execution: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return "", fmt.Errorf("failed to start command: %w", err)
	}
	observed.Outcome = evidence.Unknown
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	pgid := cmd.Process.Pid

	stdout := newCappedOutput(8 << 20)
	stderr := newCappedOutput(8 << 20)
	outWriter := io.Writer(stdout)
	errWriter := io.Writer(stderr)
	live := newLiveShellOutput(ctx)
	var finishStdout func()
	if live != nil {
		liveOut, liveErr, finish := live.Streams(pwdMarker)
		outWriter = io.MultiWriter(stdout, bestEffortLiveWriter{liveOut})
		errWriter = io.MultiWriter(stderr, bestEffortLiveWriter{liveErr})
		finishStdout = finish
	}
	outDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(outWriter, stdoutReader)
		close(outDone)
	}()
	errDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(errWriter, stderrReader)
		close(errDone)
	}()

	type waitResult struct {
		ps  *os.ProcessState
		err error
	}
	done := make(chan waitResult, 1)
	go func() {
		ps, waitErr := cmd.Process.Wait()
		done <- waitResult{ps: ps, err: waitErr}
	}()

	select {
	case <-timeoutCtx.Done():
		killProcessGroup(pgid)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = stdoutReader.Close()
		_ = stderrReader.Close()

		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}

		if timeoutCtx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("command timed out after %d seconds. If this is a server or long-running process, use 'background: true'", max(1, int(timeout.Seconds())))
		} else {
			err = fmt.Errorf("command cancelled by user")
		}

	case res := <-done:
		if res.ps != nil {
			if code := res.ps.ExitCode(); code >= 0 {
				observed.ExitCode = &code
			}
			observed.Outcome = evidence.Succeeded
			if !res.ps.Success() {
				observed.Outcome = evidence.Failed
			}
		}
		killProcessGroup(pgid)
		select {
		case <-outDone:
		case <-time.After(50 * time.Millisecond):
			_ = stdoutReader.Close()
		}
		select {
		case <-errDone:
		case <-time.After(50 * time.Millisecond):
			_ = stderrReader.Close()
		}

		if res.err != nil {
			err = res.err
		} else if res.ps != nil && !res.ps.Success() {
			err = &exec.ExitError{ProcessState: res.ps}
		}
	}

	<-outDone
	<-errDone
	_ = stdoutReader.Close()
	_ = stderrReader.Close()
	if finishStdout != nil {
		finishStdout()
		live.Finish()
	}

	if timeoutCtx.Err() == context.Canceled {
		return "", fmt.Errorf("command cancelled by user: %w", context.Canceled)
	}

	output := cleanShellOutput(stdout.String())
	errOutput := cleanShellOutput(stderr.String())
	if stdout.overflow {
		output += "\n[stdout capture limit reached at 8 MiB; later bytes discarded]\n"
	}
	if stderr.overflow {
		errOutput += "\n[stderr capture limit reached at 8 MiB; later bytes discarded]\n"
	}

	markerIdx := strings.LastIndex(output, "\n"+pwdMarker)
	isPrefixed := false
	if markerIdx == -1 && strings.HasPrefix(output, pwdMarker) {
		markerIdx = 0
		isPrefixed = true
	}
	if markerIdx != -1 {
		markerStart := markerIdx + 1
		if isPrefixed {
			markerStart = 0
		}
		lineEnd := strings.IndexByte(output[markerStart:], '\n')
		var extractedDir string
		if lineEnd == -1 {
			extractedDir = strings.TrimSpace(output[markerStart+len(pwdMarker):])
			if isPrefixed {
				output = ""
			} else {
				output = output[:markerIdx]
			}
		} else {
			extractedDir = strings.TrimSpace(output[markerStart+len(pwdMarker) : markerStart+lineEnd])
			if isPrefixed {
				output = output[markerStart+lineEnd+1:]
			} else {
				output = output[:markerIdx] + output[markerStart+lineEnd+1:]
			}
		}
		if err == nil && extractedDir != "" && persistentBash {
			if safe, sErr := ctx.SafePath(extractedDir); sErr == nil {
				if info, statErr := os.Stat(safe); statErr == nil && info.IsDir() {
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
	if provider, ok := ctx.(interface{ GetShellTimeout() time.Duration }); ok {
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
