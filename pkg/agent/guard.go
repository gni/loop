package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"loop/pkg/agent/tool"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/limits"
)

var (
	pathlibRe = regexp.MustCompile(`Path\(['"]([^'"]+)['"]\)`)
	catFileRe = regexp.MustCompile(`(?:cat|head|tail|wc)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	sedFileRe = regexp.MustCompile(`sed\s+-[a-zA-Z0-9]+\s+['"][^'"]+['"]\s+['"]?([^\s'"]+)`)
	lsDirRe   = regexp.MustCompile(`(?:ls|dir)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	redirRe   = regexp.MustCompile(`(?:^|[^0-9&])>{1,2}\s*['"]?([^\s'";|&]+)`)
	rmTouchRe = regexp.MustCompile(`(?:rm|touch|cp|mv)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'";|&]+)`)

	bashTargetRegexes = []*regexp.Regexp{pathlibRe, sedFileRe, redirRe, rmTouchRe, catFileRe, lsDirRe}
	bashModifyPrefixes = []string{
		"git commit", "git checkout", "git restore", "git reset", "git apply", "git add",
		"mkdir", "touch", "cp ", "mv ", "rm ", "npm ", "pnpm ", "yarn ", "pip ", "go get ", "cargo ", "gofmt ", "patch ",
	}
)

var (
	ConsecutiveLimit    = limits.DefaultConsecutiveLimit
	maxWindowsPerTarget = limits.DefaultWindowsPerTarget
)

type ObservationInvalidator interface {
	Forget(absPath string)
	ForgetAll()
}

type TurnExecutionGuard struct {
	targetStreaks          map[string]int
	lastTarget             string
	readWindows            map[string]map[string]bool
	invalidator            ObservationInvalidator
	pathResolver           func(string) string
	callStreaks            map[string]int
	lastCallKey            string
	failedStreaks          map[string]int
	lastFailedCmd          string
	maxConsecutiveCalls    int
	maxConsecutiveReads    int
	maxConsecutiveFailures int
	reminderThresholds     []int
}

func NewTurnExecutionGuard() *TurnExecutionGuard {
	return NewTurnExecutionGuardWithConfig(limits.ConsecutiveLimit(), []int{3, 5})
}

func NewTurnExecutionGuardWithConfig(limit int, thresholds []int) *TurnExecutionGuard {
	if limit <= 0 {
		limit = limits.ConsecutiveLimit()
	}
	if len(thresholds) == 0 {
		thresholds = []int{3, 5}
	}
	return &TurnExecutionGuard{
		targetStreaks:          make(map[string]int),
		readWindows:            make(map[string]map[string]bool),
		callStreaks:            make(map[string]int),
		failedStreaks:          make(map[string]int),
		maxConsecutiveCalls:    limit,
		maxConsecutiveReads:    limit,
		maxConsecutiveFailures: limit,
		reminderThresholds:     thresholds,
	}
}

func (g *TurnExecutionGuard) ConsecutiveIdenticalCount() int {
	if g == nil {
		return 0
	}
	return g.callStreaks[g.lastCallKey]
}

func (g *TurnExecutionGuard) SetObservationInvalidator(inv ObservationInvalidator) {
	if g != nil {
		g.invalidator = inv
	}
}

func (g *TurnExecutionGuard) SetPathResolver(resolver func(string) string) {
	if g != nil {
		g.pathResolver = resolver
	}
}

func (g *TurnExecutionGuard) resolvePath(target string) string {
	if g == nil || target == "" {
		return ""
	}
	if g.pathResolver != nil {
		if abs := g.pathResolver(target); abs != "" {
			return abs
		}
	}
	return filepath.Clean(target)
}

func (g *TurnExecutionGuard) FileReadCount(target string) int {
	if g == nil {
		return 0
	}
	return g.targetStreaks[filepath.Clean(target)]
}

func CanonicalizeArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	var val interface{}
	if err := json.Unmarshal([]byte(trimmed), &val); err != nil {
		return trimmed
	}
	if b, err := json.Marshal(val); err == nil {
		return string(b)
	}
	return trimmed
}

func (g *TurnExecutionGuard) GetAdvisoryReminder(toolName, arguments string) string {
	if g == nil {
		return ""
	}
	callKey := toolName + ":" + CanonicalizeArguments(arguments)
	streak := g.callStreaks[callKey]

	thresholds := g.reminderThresholds
	if len(thresholds) == 0 {
		thresholds = []int{3, 5}
	}

	if len(thresholds) > 0 && streak == thresholds[0] {
		return RuntimeMessage("reminder_first", "Advisory Reminder: You are repeating the exact same tool call with identical arguments. Carefully analyze the previous result before calling again: if the task is not complete, try a different approach, different search query, or different arguments instead of repeating the call.")
	}
	if len(thresholds) > 1 && streak == thresholds[1] {
		preview := arguments
		if len(preview) > 300 {
			preview = preview[:300] + "..."
		}
		return RuntimeMessagef("warning_second", "Advisory Loop Warning: Repeated tool call detected:\n- tool: %s\n- consecutive_calls: %d\n- arguments: %s\nThe repeated calls are not making progress. Do not repeat this exact call again. Inspect the latest result and choose a different action, adjust your arguments, or proceed to completion.", toolName, streak, preview)
	}
	return ""
}

func (g *TurnExecutionGuard) CheckPreExecution(toolName, arguments string) error {
	if g == nil {
		return nil
	}

	callKey := toolName + ":" + CanonicalizeArguments(arguments)

	if toolName == "bash" {
		if cmd := extractBashCommand(arguments); cmd != "" && g.failedStreaks[cmd] >= g.maxConsecutiveFailures {
			return fmt.Errorf(RuntimeMessage("loop_failing_command", "loop detected: command '%s' has failed %d times in a row. Do not re-run this failing command; address the error or use 'edit'/'write'"), cmd, g.failedStreaks[cmd])
		}
	}

	if target := g.ExtractTargetFile(toolName, arguments); target != "" && g.targetStreaks[target] >= g.maxConsecutiveReads {
		return fmt.Errorf(RuntimeMessage("loop_repeated_target", "loop detected: target '%s' has already been inspected %d times in a row and its full contents are already in context above. Do NOT re-read it. Call 'edit' or 'write' now to modify the code"), target, g.targetStreaks[target])
	}

	if g.callStreaks[callKey] >= g.maxConsecutiveCalls {
		return fmt.Errorf(RuntimeMessage("loop_identical_call", "loop detected: identical tool call repeated %d times in a row. Stop repeating the same call and choose a different action"), g.callStreaks[callKey])
	}

	return nil
}

func (g *TurnExecutionGuard) RecordPostExecution(toolName, arguments string, _ string, err error) {
	if g == nil {
		return
	}

	callKey := toolName + ":" + CanonicalizeArguments(arguments)
	target := g.ExtractTargetFile(toolName, arguments)
	var cmd string
	if toolName == "bash" {
		cmd = extractBashCommand(arguments)
	}

	if callKey == g.lastCallKey {
		g.callStreaks[callKey]++
	} else {
		g.callStreaks = map[string]int{callKey: 1}
		g.lastCallKey = callKey
	}

	newWindow := isReadWindowNew(toolName, arguments, g.readWindows)
	if target == g.lastTarget && target != "" && !newWindow {
		g.targetStreaks[target]++
	} else {
		g.targetStreaks = make(map[string]int)
		g.lastTarget = target
		if target != "" {
			g.targetStreaks[target] = 1
		}
	}

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

	isModifying := toolName == "write" || toolName == "edit" || (toolName == "bash" && isBashModifying(cmd) && err == nil)
	if isModifying {
		g.targetStreaks = make(map[string]int)
		g.lastTarget = ""
		g.readWindows = make(map[string]map[string]bool)

		if err == nil && g.invalidator != nil {
			if target != "" {
				g.invalidator.Forget(g.resolvePath(target))
			} else if toolName == "bash" {
				g.invalidator.ForgetAll()
			}
		}
	}
}

func isReadWindowNew(toolName, arguments string, windows map[string]map[string]bool) bool {
	if toolName != "read" || windows == nil {
		return false
	}

	target := extractPathArg(arguments, "")
	if target == "" {
		return false
	}

	var args struct {
		Offset int `json:"offset"`
		Limit  int `json:"limit"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return false
	}

	windowKey := fmt.Sprintf("%d:%d", args.Offset, args.Limit)
	seen, ok := windows[target]
	if !ok {
		seen = make(map[string]bool)
		windows[target] = seen
	}

	if seen[windowKey] {
		return false
	}

	if len(seen) >= limits.WindowsPerTarget() {
		for key := range seen {
			delete(seen, key)
			break
		}
	}

	seen[windowKey] = true
	return true
}

func (g *TurnExecutionGuard) ExtractTargetFile(toolName, arguments string) string {
	switch toolName {
	case "read", "write", "edit":
		return extractPathArg(arguments, "")
	case "list", "ls":
		return extractPathArg(arguments, ".")
	case "bash":
		cmd := extractBashCommand(arguments)
		for _, re := range bashTargetRegexes {
			if m := re.FindStringSubmatch(cmd); len(m) > 1 {
				cand := strings.TrimSpace(m[1])
				if cand != "" && !strings.HasPrefix(cand, "/dev/") && !strings.HasPrefix(cand, "-") {
					return filepath.Clean(cand)
				}
			}
		}
	}
	return ""
}

func extractPathArg(arguments string, fallback string) string {
	if p := filetool.ExtractFilePath(arguments); p != "" {
		return filepath.Clean(p)
	}
	return fallback
}

func extractBashCommand(arguments string) string {
	var raw string
	var args struct {
		Command   string `json:"command"`
		Cmd       string `json:"cmd"`
		Arguments string `json:"arguments"`
	}
	_ = tool.ParseToolArguments(arguments, &args, func(s string) { raw = s })
	if v := strings.TrimSpace(tool.FirstNonEmpty(raw, args.Command, args.Cmd, args.Arguments)); v != "" {
		return v
	}
	return strings.TrimSpace(arguments)
}

func isBashModifying(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return false
	}
	for _, p := range bashModifyPrefixes {
		if strings.HasPrefix(trimmed, p) {
			return true
		}
	}
	return strings.Contains(trimmed, "sed -i") || strings.Contains(trimmed, " > ") || strings.Contains(trimmed, " >> ") || strings.Contains(trimmed, "> ")
}