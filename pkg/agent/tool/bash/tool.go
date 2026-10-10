package bash

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	domaintool "loop/pkg/domain/tool"
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
		"Use dedicated tools for file operations (read, edit, write, grep, find) instead of shell commands.",
		"Working directory persists across sequential commands in the session. Use 'dir' to execute in a specific directory.",
		"For background processes and long-running services, set 'background': true.",
	})
}

func (t *bashTool) Definition() domaintool.Tool {
	return domaintool.NewFunctionTool(
		"bash",
		domaintool.FormatToolDescription("bash", "Execute shell commands inside the workspace (builds, tests, package installation, git commands, and process management). Working directory persists across commands."),
		map[string]domaintool.SchemaProp{
			"command":    domaintool.StringProp(domaintool.FormatParamDescription("bash", "command", "The command to run in the terminal")),
			"dir":        domaintool.StringProp(domaintool.FormatParamDescription("bash", "dir", "Optional working directory in which to execute the command. Persists across commands for this session.")),
			"background": domaintool.BoolProp(domaintool.FormatParamDescription("bash", "background", "Set to true to run the command in the background as a tracked background task (for servers, daemons, or long-running tasks).")),
		},
		"command",
	)
}

func (t *bashTool) Execute(ctx domaintool.AgentContext, arguments string) (string, error) {
	var args struct {
		Command    string `json:"command"`
		Cmd        string `json:"cmd"`
		Arguments  string `json:"arguments"`
		Dir        string `json:"dir"`
		Cwd        string `json:"cwd"`
		Background bool   `json:"background"`
	}
	if err := domaintool.ParseToolArguments(arguments, &args, func(s string) { args.Command = s }); err != nil {
		return "", err
	}
	args.Command = domaintool.FirstNonEmpty(args.Command, args.Cmd, args.Arguments)
	if args.Command == "" {
		return "", fmt.Errorf("command parameter is empty")
	}

	args.Dir = domaintool.FirstNonEmpty(args.Dir, args.Cwd)

	t.mu.Lock()
	defer t.mu.Unlock()

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
			bgCmd = fmt.Sprintf("cd %q && %s", workDir, bgCmd)
		}
		id, err := ctx.SpawnTask(bgCmd, os.Stderr)
		if err != nil {
			return "", fmt.Errorf("failed to spawn background task: %w", err)
		}
		return fmt.Sprintf("Task spawned in background with ID: %s. You can monitor its output using 'task_status' or kill it using 'task_kill'. Toggle live stream via Ctrl+O.", id), nil
	}

	timeoutCtx, cancel := context.WithTimeout(ctx.Context(), 120*time.Second)
	defer cancel()

	wrappedCmd := fmt.Sprintf("%s\n__LOOP_RET=$?\necho \"__LOOP_PWD:$(pwd -P)\"\nexit $__LOOP_RET", args.Command)
	cmd := exec.Command("bash", "-c", wrappedCmd)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	if err := cmd.Start(); err != nil {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
		return "", fmt.Errorf("failed to start command: %w", err)
	}
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	pgid := cmd.Process.Pid

	var stdout, stderr bytes.Buffer
	outDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stdout, stdoutReader)
		close(outDone)
	}()
	errDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderr, stderrReader)
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
		if pgid > 0 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
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
			err = fmt.Errorf("command timed out after 120 seconds. If this is a server or long-running process, use 'background: true'")
		} else {
			err = fmt.Errorf("command cancelled by user")
		}

	case res := <-done:
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

	output := strings.ToValidUTF8(stdout.String(), "\uFFFD")
	errOutput := strings.ToValidUTF8(stderr.String(), "\uFFFD")

	const pwdMarker = "__LOOP_PWD:"
	if idx := strings.LastIndex(output, pwdMarker); idx != -1 {
		lineEnd := strings.Index(output[idx:], "\n")
		var extractedDir string
		if lineEnd == -1 {
			extractedDir = strings.TrimSpace(output[idx+len(pwdMarker):])
			output = output[:idx]
		} else {
			extractedDir = strings.TrimSpace(output[idx+len(pwdMarker) : idx+lineEnd])
			output = output[:idx] + output[idx+lineEnd+1:]
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

	combined = TruncateOutput(combined)

	if err != nil {
		exitDesc := FormatExitError(err, timeoutCtx)
		if strings.TrimSpace(combined) == "" {
			combined = fmt.Sprintf("%s (no output on stdout or stderr)", exitDesc)
		} else if !strings.Contains(combined, "exit status") && !strings.Contains(combined, "exit code") {
			combined = fmt.Sprintf("%s\n\n(%s)", strings.TrimRight(combined, "\r\n"), exitDesc)
		}
		return combined, fmt.Errorf("%s", exitDesc)
	}
	return combined, nil
}
