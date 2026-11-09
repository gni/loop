package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/process"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

const maxManualCommandCaptureBytes = 512 << 10

func sanitizedCommandOutput(buffer *process.Buffer) string {
	out := tool.SanitizeUTF8([]byte(buffer.String()))
	if buffer.Overflow {
		out += "\n...[output truncated]..."
	}
	return out
}

func parseManualCommand(line string, enabled bool) (bool, string) {
	line = strings.TrimLeft(line, " \t")
	if strings.HasPrefix(line, "!") {
		return true, strings.TrimLeft(strings.TrimPrefix(line, "!"), " \t")
	}
	if !enabled {
		return false, ""
	}
	parts := strings.Fields(line)
	if len(parts) > 0 && (parts[0] == "ls" || parts[0] == "cd") {
		return true, line
	}
	return false, ""
}

func (shell *manualShell) execute(
	cmdStr string,
	line string,
	promptPrefix string,
	a *agent.Agent,
	ppWriter *ui.PromptPreservingWriter,
	messages *[]db.Message,
	currentSessionID string,
	kiReader *interceptor.KeyInterceptorReader,
	theme style.UITheme,
) {
	if strings.TrimSpace(cmdStr) == "" {
		fmt.Fprintln(ppWriter, "enter a command after !")
		return
	}
	target, isPureCd, directoryErr := directoryCommand(cmdStr)

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	ppWriter.BeginScrollEntry()
	fmt.Fprintf(ppWriter, "%s%s\n", promptStyle.Render(promptPrefix), line)

	if isPureCd {
		if directoryErr != nil {
			fmt.Fprintf(ppWriter, "cd: %v\n", directoryErr)
			return
		}
		directory, err := shell.changeDirectory(target)
		if err != nil {
			fmt.Fprintf(ppWriter, "cd: %v\n", err)
			return
		}
		fmt.Fprintf(ppWriter, "changed directory to: %s\n", directory)
		contextMsg := fmt.Sprintf("[user changed the shell directory for manual commands to: `%s`]", directory)
		msg := db.Message{Role: "user", Content: contextMsg}
		if err := a.AppendInteractiveMessage(messages, currentSessionID, msg); err != nil {
			fmt.Fprintf(ppWriter, "could not save command history: %v\n", err)
		}
		return
	}

	if !a.IsShellExecutionAllowed() {
		fmt.Fprintln(ppWriter, "shell execution requires a trusted workspace; command not run")
		return
	}

	ctx, cancel := context.WithCancel(a.Context())
	ui.GetUI().StateMu.Lock()
	ui.GetUI().ActiveCancelFunc = cancel
	ui.GetUI().StateMu.Unlock()
	ui.TerminalMu.Lock()
	ui.DrawConsoleStaticControlsLocked(os.Stderr, a, kiReader, nil, true)
	ui.TerminalMu.Unlock()
	defer func() {
		cancel()
		ui.GetUI().StateMu.Lock()
		ui.GetUI().ActiveCancelFunc = nil
		ui.GetUI().StateMu.Unlock()
	}()

	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			fmt.Fprintf(ppWriter, "terminal: %v\n", err)
			return
		}
		defer term.Restore(fd, oldState)
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", cmdStr)
	process.SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = shell.directory
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	for key, value := range a.GetExecutionEnv() {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdout := &process.Buffer{Limit: maxManualCommandCaptureBytes}
	stderr := &process.Buffer{Limit: maxManualCommandCaptureBytes}
	cw := interceptor.CRNLWriter{W: ppWriter}
	console := render.NewShellOutputWriter(cw, cmdStr, theme)
	cmd.Stdout = io.MultiWriter(interceptor.NewSafeShellOutputWriter(console), stdout)
	cmd.Stderr = io.MultiWriter(interceptor.NewSafeShellOutputWriter(console), stderr)
	stopIndicator := startManualCommandIndicator(theme, a.Config != nil && a.Config.ShowMicroseconds)
	err := cmd.Run()
	_ = process.KillProcessGroup(cmd)
	stopIndicator()

	if a.FileObservations != nil {
		a.FileObservations.ForgetAll()
	}

	output := sanitizedCommandOutput(stdout)
	errOutput := sanitizedCommandOutput(stderr)

	if err != nil {
		fmt.Fprintf(ppWriter, "command failed: %v\n", err)
	}

	var combined string
	if output != "" {
		combined += fmt.Sprintf("stdout:\n%s\n", output)
	}
	if errOutput != "" {
		combined += fmt.Sprintf("stderr:\n%s\n", errOutput)
	}
	if err != nil {
		combined += fmt.Sprintf("error:\n%v\n", err)
	}
	if combined == "" {
		combined = "(command completed with no output)"
	}

	contextMsg := fmt.Sprintf("[user manually executed local shell command: `%s`]\n%s", cmdStr, combined)
	msg := db.Message{Role: "user", Content: contextMsg}
	if err := a.AppendInteractiveMessage(messages, currentSessionID, msg); err != nil {
		fmt.Fprintf(ppWriter, "could not save command history: %v\n", err)
	}

	ppWriter.EnsureNewline()
}

func startManualCommandIndicator(theme style.UITheme, showMicroseconds bool) func() {
	start := time.Now()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	elapsedText := func() string {
		return style.NewStyle().Foreground(theme.Border).Render(fmt.Sprintf("shell (%s)", style.FormatElapsedDuration(time.Since(start), showMicroseconds)))
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(240 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ui.DrawStaticStatsLine(os.Stderr, theme, style.ActivitySpinnerFrame(theme, frame), elapsedText())
				frame++
			}
		}
	}()
	return func() {
		close(stop)
		<-stopped
		ui.DrawStaticStatsLine(os.Stderr, theme, "", elapsedText())
	}
}
