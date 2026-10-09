package repl

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"loop/pkg/agent"
	"loop/pkg/agent/swarm"
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
	if len(parts) == 0 {
		return false, ""
	}

	firstWord := parts[0]
	directCommands := map[string]bool{
		"ls": true,
		"cd": true,
	}

	if directCommands[firstWord] {
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
	rl *term.Terminal,
	allowedTools []string,
	mam *swarm.MultiAgentManager,
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

	if isPureCd {
		if target == "" {
			home, err := os.UserHomeDir()
			if err == nil {
				target = home
			}
		} else {
			if (strings.HasPrefix(target, "\"") && strings.HasSuffix(target, "\"")) ||
				(strings.HasPrefix(target, "'") && strings.HasSuffix(target, "'")) {
				if len(target) >= 2 {
					target = target[1 : len(target)-1]
				}
			}
			if target == "~" {
				home, err := os.UserHomeDir()
				if err == nil {
					target = home
				}
			} else if strings.HasPrefix(target, "~/") {
				home, err := os.UserHomeDir()
				if err == nil {
					target = filepath.Join(home, target[2:])
				}
			} else if strings.HasPrefix(target, "~\\") {
				home, err := os.UserHomeDir()
				if err == nil {
					target = filepath.Join(home, target[2:])
				}
			}
		}

		promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
		ppWriter.ForceReposition()
		fmt.Fprintf(ppWriter, "%s%s\n", promptStyle.Render(promptPrefix), line)
		err := os.Chdir(target)
		if err != nil {
			fmt.Fprintf(ppWriter, "cd: %v\n", err)
		} else {
			pwd, _ := os.Getwd()
			a.WorkspaceRoot = pwd
			fmt.Fprintf(ppWriter, "changed directory to: %s\n", pwd)
		}
		contextMsg := fmt.Sprintf("[user manually changed working directory to: `%s`]", target)
		*messages = append(*messages, db.Message{Role: "user", Content: contextMsg})
		_ = db.SaveMessage(currentSessionID, (*messages)[len(*messages)-1])
		ppWriter.ForceReposition()
		kiReader.Drain()
		activeTasks := a.CountActiveTasks()
		pTok, cTok, estimated := interceptor.CalculateActiveTokenUsage(a, *messages, allowedTools, mam)
		latestTurnTokens := a.GetLatestAssistantCompletionTokens(*messages)
		effLimit := a.GetEffectiveContextLimit(pTok)
		ui.UpdateStatus(a.Config.Model, pTok, cTok, latestTurnTokens, effLimit, false, 0, activeTasks, a.Config.ShowTokens, estimated)
		ui.RefreshConsoleAfterTurn(os.Stderr, a, kiReader, rl)
		return
	}

	promptStyle := style.NewStyle().Foreground(theme.Primary).Bold(true)
	ppWriter.ForceReposition()
	fmt.Fprintf(ppWriter, "%s%s\n", promptStyle.Render(promptPrefix), line)

	cmd := exec.Command("bash", "-c", cmdStr)
	cmd.Dir = a.WorkspaceRoot
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C.UTF-8")
	var stdout, stderr bytes.Buffer
	cw := interceptor.CRNLWriter{W: ppWriter}
	cmd.Stdout = io.MultiWriter(cw, &stdout)
	cmd.Stderr = io.MultiWriter(cw, &stderr)
	cmd.Stdin = os.Stdin
	err := cmd.Run()

	output := tool.SanitizeUTF8(stdout.Bytes())
	errOutput := tool.SanitizeUTF8(stderr.Bytes())

	if err != nil {
		fmt.Fprintf(ppWriter, "command failed: %v\n", err)
	}

	combined := ""
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

	successStyle := style.NewStyle().Foreground(theme.Success).Italic(true)
	fmt.Fprintln(ppWriter)
	fmt.Fprintln(ppWriter, successStyle.Render("command output appended to conversation context."))
	ppWriter.ForceReposition()
	kiReader.Drain()
	refreshREPLStatus(a, *messages, allowedTools, mam, kiReader, rl, false)
}
