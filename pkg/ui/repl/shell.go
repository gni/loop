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
	"time"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/tool"
	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/process"
	"loop/pkg/ui"
	"loop/pkg/ui/interceptor"
	"loop/pkg/ui/render"
	"loop/pkg/ui/style"
)

const maxManualCommandCaptureBytes = 512 << 10

type limitedCaptureBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedCaptureBuffer) Write(p []byte) (int, error) {
	total := len(p)
	if b.truncated || b.limit <= 0 {
		b.truncated = true
		return total, nil
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return total, nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return total, nil
	}
	_, _ = b.buf.Write(p)
	return total, nil
}

func (b *limitedCaptureBuffer) SanitizedString() string {
	out := tool.SanitizeUTF8(b.buf.Bytes())
	if b.truncated {
		out += "\n...[output truncated]..."
	}
	return out
}

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
	ppWriter.BeginScrollEntry()
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

		if !filepath.IsAbs(target) {
			target = filepath.Join(a.WorkspaceRoot, target)
		}
		cleanTarget := filepath.Clean(target)
		info, err := os.Stat(cleanTarget)
		if err != nil || !info.IsDir() {
			if err == nil {
				err = fmt.Errorf("not a directory")
			}
			fmt.Fprintf(ppWriter, "cd: %v\n", err)
			return
		}
		if resolved, evalErr := filepath.EvalSymlinks(cleanTarget); evalErr == nil && resolved != "" {
			cleanTarget = resolved
		}
		a.WorkspaceRoot = cleanTarget
		if a.Config != nil {
			trusted := config.WorkspaceTrusted(a.ConfigPath, cleanTarget)
			a.Config.ProjectResources = trusted
		}
		fmt.Fprintf(ppWriter, "changed directory to: %s\n", cleanTarget)
		contextMsg := fmt.Sprintf("[user manually changed working directory to: `%s`]", cleanTarget)
		msg := db.Message{Role: "user", Content: contextMsg}
		a.MessagesMu.Lock()
		*messages = append(*messages, msg)
		a.MessagesMu.Unlock()
		if !a.Ephemeral && currentSessionID != "" {
			_ = db.SaveMessage(currentSessionID, msg)
		}
		return
	}

	if !a.IsShellExecutionAllowed() {
		fmt.Fprintln(ppWriter, "shell execution requires a trusted workspace; command not run")
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
	process.SetupProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	stdout := &limitedCaptureBuffer{limit: maxManualCommandCaptureBytes}
	stderr := &limitedCaptureBuffer{limit: maxManualCommandCaptureBytes}
	cw := interceptor.CRNLWriter{W: ppWriter}
	console := render.NewShellOutputWriter(cw, cmdStr, theme)
	cmd.Stdout = io.MultiWriter(interceptor.NewSafeShellOutputWriter(console), stdout)
	cmd.Stderr = io.MultiWriter(interceptor.NewSafeShellOutputWriter(console), stderr)
	stopIndicator := startManualCommandIndicator(theme, a.Config != nil && a.Config.ShowMicroseconds)
	err := cmd.Run()
	stopIndicator()

	if a.FileObservations != nil {
		a.FileObservations.ForgetAll()
	}

	output := stdout.SanitizedString()
	errOutput := stderr.SanitizedString()

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
	a.MessagesMu.Lock()
	*messages = append(*messages, msg)
	a.MessagesMu.Unlock()
	if !a.Ephemeral && currentSessionID != "" {
		_ = db.SaveMessage(currentSessionID, msg)
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
