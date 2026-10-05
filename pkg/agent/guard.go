package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	pathlibRe = regexp.MustCompile(`Path\(['"]([^'"]+)['"]\)`)
	catFileRe = regexp.MustCompile(`(?:cat|head|tail|wc)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	sedFileRe = regexp.MustCompile(`sed\s+-[a-zA-Z0-9]+\s+['"][^'"]+['"]\s+['"]?([^\s'"]+)`)
	lsDirRe   = regexp.MustCompile(`(?:ls|dir)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
)

// ConsecutiveLimit is the number of identical calls in a row required before
// the guard blocks. Streaks are measured back-to-back, never accumulated over
// a turn or session: a call followed by any different call resets the streak.
const ConsecutiveLimit = 10

// TurnExecutionGuard blocks tool calls that are repeated back-to-back past
// ConsecutiveLimit. Every counter tracks a consecutive streak, not a total, so
// a model that inspects the same file ten times across a long session is never
// penalised as long as the calls are interleaved with other work.
type TurnExecutionGuard struct {
	targetStreaks          map[string]int
	lastTarget             string
	callStreaks            map[string]int
	lastCallKey            string
	failedStreaks          map[string]int
	lastFailedCmd          string
	maxConsecutiveCalls    int
	maxConsecutiveReads    int
	maxConsecutiveFailures int
}

// NewTurnExecutionGuard creates a new execution guard for an active turn.
func NewTurnExecutionGuard() *TurnExecutionGuard {
	return &TurnExecutionGuard{
		targetStreaks:          make(map[string]int),
		callStreaks:            make(map[string]int),
		failedStreaks:          make(map[string]int),
		maxConsecutiveCalls:    ConsecutiveLimit,
		maxConsecutiveReads:    ConsecutiveLimit,
		maxConsecutiveFailures: ConsecutiveLimit,
	}
}

// ConsecutiveIdenticalCount returns the current consecutive identical tool call streak.
func (g *TurnExecutionGuard) ConsecutiveIdenticalCount() int {
	if g == nil {
		return 0
	}
	return g.callStreaks[g.lastCallKey]
}

// FileReadCount returns the current consecutive inspection streak for target.
func (g *TurnExecutionGuard) FileReadCount(target string) int {
	if g == nil {
		return 0
	}
	return g.targetStreaks[filepath.Clean(target)]
}

// CheckPreExecution evaluates a proposed tool call BEFORE execution.
// Only back-to-back repetition past ConsecutiveLimit is blocked; a call that
// is interleaved with other work is always allowed.
func (g *TurnExecutionGuard) CheckPreExecution(toolName, arguments string) error {
	if g == nil {
		return nil
	}

	trimmedArgs := strings.TrimSpace(arguments)
	callKey := toolName + ":" + trimmedArgs

	// 1. Same failing command run ConsecutiveLimit times in a row.
	if toolName == "bash" {
		cmd := extractBashCommand(arguments)
		if cmd != "" && g.failedStreaks[cmd] >= g.maxConsecutiveFailures {
			return fmt.Errorf("loop detected: command '%s' has failed %d times in a row. Do not re-run this failing command; address the error or use 'edit'/'write'", cmd, g.failedStreaks[cmd])
		}
	}

	// 2. Same target inspected ConsecutiveLimit times in a row.
	target := g.ExtractTargetFile(toolName, arguments)
	if target != "" && g.targetStreaks[target] >= g.maxConsecutiveReads {
		return fmt.Errorf("loop detected: target '%s' has already been inspected %d times in a row and its full contents are already in context above. Do NOT re-read it. Call 'edit' or 'write' now to modify the code", target, g.targetStreaks[target])
	}

	// 3. Same tool call executed ConsecutiveLimit times in a row.
	if g.callStreaks[callKey] >= g.maxConsecutiveCalls {
		return fmt.Errorf("loop detected: identical tool call repeated %d times in a row. Stop repeating the same call and choose a different action", g.callStreaks[callKey])
	}

	return nil
}

// RecordPostExecution updates streak counters after a tool call completes.
// Streaks are strictly back-to-back: any call that differs from the previous
// one clears every streak, so counts never accumulate across a turn or session.
func (g *TurnExecutionGuard) RecordPostExecution(toolName, arguments string, output string, err error) {
	if g == nil {
		return
	}

	trimmedArgs := strings.TrimSpace(arguments)
	callKey := toolName + ":" + trimmedArgs
	target := g.ExtractTargetFile(toolName, arguments)
	cmd := cmdForTool(toolName, arguments)

	// Identical call: extend the streak.
	if callKey == g.lastCallKey {
		g.callStreaks[callKey]++
	} else {
		g.callStreaks = make(map[string]int)
		g.lastCallKey = callKey
		g.callStreaks[callKey] = 1
	}

	// Same target in a row: extend the inspection streak. Any call on a
	// different target (including targetless work like grep) ends it.
	if target == g.lastTarget && target != "" {
		g.targetStreaks[target]++
	} else {
		g.targetStreaks = make(map[string]int)
		g.lastTarget = target
		if target != "" {
			g.targetStreaks[target] = 1
		}
	}

	// Same failing command in a row: extend the failure streak. A different
	// command, or a success, ends it.
	if toolName == "bash" && cmd != "" {
		if err != nil && cmd == g.lastFailedCmd {
			g.failedStreaks[cmd]++
		} else {
			g.failedStreaks = make(map[string]int)
			g.lastFailedCmd = ""
			if err != nil {
				g.lastFailedCmd = cmd
				g.failedStreaks[cmd] = 1
			}
		}
	}

	// A modification invalidates previous inspections: the file content changed,
	// so re-reading it is legitimate again.
	isModifying := toolName == "write" || toolName == "edit" || (toolName == "bash" && isBashModifying(cmd) && err == nil)
	if isModifying {
		g.targetStreaks = make(map[string]int)
		g.lastTarget = ""
	}
}

func cmdForTool(toolName, arguments string) string {
	if toolName != "bash" {
		return ""
	}
	return extractBashCommand(arguments)
}

// ExtractTargetFile extracts the file or directory path being inspected by read, list, or bash commands.
func (g *TurnExecutionGuard) ExtractTargetFile(toolName, arguments string) string {
	if toolName == "read" {
		var args struct {
			Path     string `json:"path"`
			File     string `json:"file"`
			FilePath string `json:"file_path"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Path != "" {
				return filepath.Clean(args.Path)
			}
			if args.FilePath != "" {
				return filepath.Clean(args.FilePath)
			}
			if args.File != "" {
				return filepath.Clean(args.File)
			}
		}
		var raw string
		if err := json.Unmarshal([]byte(arguments), &raw); err == nil && raw != "" {
			return filepath.Clean(raw)
		}
		return ""
	}

	if toolName == "list" || toolName == "ls" {
		var args struct {
			Path    string `json:"path"`
			Dir     string `json:"dir"`
			DirPath string `json:"dir_path"`
		}
		if err := json.Unmarshal([]byte(arguments), &args); err == nil {
			if args.Path != "" {
				return filepath.Clean(args.Path)
			}
			if args.Dir != "" {
				return filepath.Clean(args.Dir)
			}
			if args.DirPath != "" {
				return filepath.Clean(args.DirPath)
			}
		}
		var raw string
		if err := json.Unmarshal([]byte(arguments), &raw); err == nil && raw != "" {
			return filepath.Clean(raw)
		}
		return "."
	}

	if toolName == "bash" {
		cmd := extractBashCommand(arguments)
		if m := pathlibRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := sedFileRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := catFileRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
		if m := lsDirRe.FindStringSubmatch(cmd); len(m) > 1 {
			return filepath.Clean(m[1])
		}
	}

	return ""
}

func extractBashCommand(arguments string) string {
	var args struct {
		Command   string `json:"command"`
		Cmd       string `json:"cmd"`
		Arguments string `json:"arguments"`
	}
	trimmed := strings.TrimSpace(arguments)
	if strings.HasPrefix(trimmed, "\"") && strings.HasSuffix(trimmed, "\"") && len(trimmed) >= 2 {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err == nil {
			return strings.TrimSpace(unquoted)
		}
	}
	if err := json.Unmarshal([]byte(arguments), &args); err == nil {
		if args.Command != "" {
			return strings.TrimSpace(args.Command)
		}
		if args.Cmd != "" {
			return strings.TrimSpace(args.Cmd)
		}
		if args.Arguments != "" {
			return strings.TrimSpace(args.Arguments)
		}
	}
	return trimmed
}

func isBashModifying(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	prefixes := []string{"git commit", "git add", "mkdir", "touch", "cp ", "mv ", "rm ", "npm ", "pnpm ", "yarn ", "pip ", "go get ", "cargo "}
	for _, p := range prefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	if strings.Contains(trimmed, "sed -i") || strings.Contains(trimmed, " > ") || strings.Contains(trimmed, " >> ") || strings.Contains(trimmed, "> ") {
		return true
	}
	return false
}
