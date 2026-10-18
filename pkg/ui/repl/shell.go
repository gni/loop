package repl

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/db"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/style"
)

func parseManualCommand(line string, enabled bool) (bool, string) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "!") {
		return true, strings.TrimSpace(strings.TrimPrefix(line, "!"))
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

func executeManualShellCommand(
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
	isPureCd := false
	var target string
	if cmdStr == "cd" {
		isPureCd = true
	} else if strings.HasPrefix(cmdStr, "cd ") {
		rest := strings.TrimSpace(cmdStr[3:])
		if !strings.Contains(rest, "&&") && !strings.Contains(rest, ";") && !strings.Contains(rest, "|") {
			isPureCd = true
			target = rest
		}
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	ppWriter.EnsureNewline()
	ppWriter.ForceReposition()
	fmt.Fprintf(ppWriter, "%s%s\n", promptStyle.Render(promptPrefix), line)

	if isPureCd {
		target = strings.Trim(target, "\"'")
		if target == "" || target == "~" {
			if home, err := os.UserHomeDir(); err == nil {
				target = home
			}
		} else if strings.HasPrefix(target, "~/") || strings.HasPrefix(target, "~\\") {
			if home, err := os.UserHomeDir(); err == nil {
				target = filepath.Join(home, target[2:])
			}
		}

		if err := os.Chdir(target); err != nil {
			fmt.Fprintf(ppWriter, "cd: %v\n", err)
		} else {
			pwd, _ := os.Getwd()
			a.WorkspaceRoot = pwd
			fmt.Fprintf(ppWriter, "changed directory to: %s\n", pwd)
		}
		contextMsg := fmt.Sprintf("[user manually changed working directory to: `%s`]", target)
		*messages = append(*messages, db.Message{Role: "user", Content: contextMsg})
		_ = db.SaveMessage(currentSessionID, (*messages)[len(*messages)-1])
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	var stdout, stderr bytes.Buffer
	cw := interceptor.CRNLWriter{W: ppWriter}
	cmd.Stdout = io.MultiWriter(cw, &stdout)
	cmd.Stderr = io.MultiWriter(cw, &stderr)
	stopIndicator := startManualCommandIndicator(theme)
	err := cmd.Run()
	stopIndicator()

	if a.FileObservations != nil {
		a.FileObservations.ForgetAll()
	}

	output := tool.SanitizeUTF8(stdout.Bytes())
	errOutput := tool.SanitizeUTF8(stderr.Bytes())

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
	*messages = append(*messages, db.Message{Role: "user", Content: contextMsg})
	_ = db.SaveMessage(currentSessionID, (*messages)[len(*messages)-1])

	ppWriter.EnsureNewline()
}

func startManualCommandIndicator(theme style.UITheme) func() {
	start := time.Now()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	elapsedText := func() string {
		return style.NewStyle().Foreground(theme.Border).Render(fmt.Sprintf("shell (%.1fs)", time.Since(start).Seconds()))
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
