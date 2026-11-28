package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"loop/pkg/agent/tool"
	bashtool "loop/pkg/agent/tool/bash"
	filetool "loop/pkg/agent/tool/file"
	"loop/pkg/domain/evidence"
	"loop/pkg/domain/limits"
)

var (
	pathlibRe  = regexp.MustCompile(`Path\(['"]([^'"]+)['"]\)`)
	catFileRe  = regexp.MustCompile(`(?:cat|head|tail|wc)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	sedFileRe  = regexp.MustCompile(`sed\s+-[a-zA-Z0-9]+\s+['"][^'"]+['"]\s+['"]?([^\s'"]+)`)
	lsDirRe    = regexp.MustCompile(`(?:ls|dir)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'"]+)`)
	redirRe    = regexp.MustCompile(`(?:^|[^0-9&])>{1,2}\s*['"]?([^\s'";|&]+)`)
	rmTouchRe  = regexp.MustCompile(`(?:rm|touch|cp|mv)\s+(?:-[a-zA-Z0-9]+\s+)*['"]?([^\s'";|&]+)`)
	cmdSplitRe = regexp.MustCompile(`(?:&&|\|\||;|\|)`)

	bashTargetRegexes  = []*regexp.Regexp{pathlibRe, sedFileRe, redirRe, rmTouchRe, catFileRe, lsDirRe}
	bashModifyPrefixes = []string{
		"git commit", "git checkout", "git restore", "git reset", "git apply", "git add",
		"git stash push", "git stash pop", "git stash apply", "git stash drop", "git stash clear",
		"mkdir", "touch", "cp ", "mv ", "rm ",
		"npm install", "npm i ", "npm ci", "npm uninstall", "npm update", "npm init",
		"pnpm install", "pnpm add", "pnpm remove", "pnpm update",
		"yarn add", "yarn remove", "yarn install", "yarn upgrade",
		"pip install", "pip uninstall", "pip3 install", "pip3 uninstall",
		"go get ", "go mod tidy", "go mod init", "go mod edit", "go mod download",
		"cargo add", "cargo remove", "cargo update", "cargo fmt", "cargo fix", "cargo new", "cargo init",
		"gofmt -w", "patch ", "tee ",
	}
)

type ObservationInvalidator interface {
	Forget(absPath string)
	ForgetAll()
}

type TurnExecutionGuard struct {
	outcomes               map[string]observedOutcome
	outcomeOrder           []string
	targetStreak           int
	lastTarget             string
	readWindows            map[string]map[string]bool
	invalidator            ObservationInvalidator
	callStreak             int
	lastCallKey            string
	failureStreak          int
	lastFailedKey          string
	maxConsecutiveCalls    int
	maxConsecutiveReads    int
	maxConsecutiveFailures int
	reminderThresholds     []int
}

func NewTurnExecutionGuardWithConfig(limit int, thresholds []int) *TurnExecutionGuard {
	if limit <= 0 {
		limit = limits.ConsecutiveLimit()
	}
	if len(thresholds) == 0 {
		thresholds = []int{3, 5}
	}
	return &TurnExecutionGuard{
		readWindows:            make(map[string]map[string]bool),
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
	return g.callStreak
}

func (g *TurnExecutionGuard) SetObservationInvalidator(inv ObservationInvalidator) {
	if g != nil {
		g.invalidator = inv
	}
}

func CanonicalizeToolArguments(toolName, arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}
	return tool.CanonicalArguments(toolName, trimmed)
}

func buildCallKey(toolName, arguments string) string {
	// Use the same aliases and wrappers as Prepare so checks and recorded
	// results identify the same call before and after argument normalization.
	canonical := CanonicalizeToolArguments(toolName, arguments)
	return fmt.Sprintf("%s:%x", toolName, sha256.Sum256([]byte(canonical)))
}

func (g *TurnExecutionGuard) GetAdvisoryReminder(toolName, arguments string) string {
	if g == nil {
		return ""
	}
	key := buildCallKey(toolName, arguments)
	streak := g.outcomes[key].repeats
	if key == g.lastCallKey {
		streak = max(g.callStreak, streak)
	}

	thresholds := g.reminderThresholds
	if len(thresholds) == 0 {
		thresholds = []int{3, 5}
	}

	outcome := g.outcomes[key]
	if toolName == "task_state" && !outcome.failed && (streak == thresholds[0] || (len(thresholds) > 1 && streak == thresholds[1])) {
		return "Request progress is unchanged. Continue the work or give the final result instead of repeating this assessment."
	}
	if outcome.noProgress && (outcome.repeats == thresholds[0] || (len(thresholds) > 1 && outcome.repeats == thresholds[1])) {
		return "This action has repeatedly left the file unchanged. Reuse the current contents or revise the requested change before trying again."
	}
	if isStatusPollingTool(toolName) && (outcome.repeats == thresholds[0] || (len(thresholds) > 1 && outcome.repeats == thresholds[1])) {
		return "The background status has not changed. Allow the task time to run, or work on another independent step before polling again."
	}
	if (outcome.failed || tool.PolicyForName(toolName, arguments).ReadOnly) && (outcome.repeats == thresholds[0] || (len(thresholds) > 1 && outcome.repeats == thresholds[1])) {
		if outcome.failed {
			return fmt.Sprintf("The same failure has occurred %d times. Use the diagnostic to correct its cause before another attempt; alternating other tools does not resolve this failure.", outcome.repeats)
		}
		return fmt.Sprintf("This inspection has returned unchanged evidence %d times. Reuse it; change the search query or read offset only when you need new information.", outcome.repeats)
	}
	if len(thresholds) > 0 && streak == thresholds[0] {
		return RuntimeMessage("reminder_first", "Advisory Reminder: You are repeating the exact same tool call with identical arguments. Carefully analyze the previous result before calling again: if the task is not complete, try a different approach, different search query, or different arguments instead of repeating the call.")
	}
	if len(thresholds) > 1 && streak == thresholds[1] {
		preview := arguments
		if len(preview) > 300 {
			preview = filetool.UTF8SafePrefix(preview, 300) + "..."
		}
		return RuntimeMessagef("warning_second", "Advisory Loop Warning: Repeated tool call detected:\n- tool: %s\n- consecutive_calls: %d\n- arguments: %s\nThe repeated calls are not making progress. Do not repeat this exact call again. Inspect the latest result and choose a different action, adjust your arguments, or proceed to completion.", toolName, streak, preview)
	}
	return ""
}

func (g *TurnExecutionGuard) CheckPreExecution(toolName, arguments string) (guardErr error) {
	defer func() {
		if guardErr != nil {
			guardErr = &RepetitionError{Cause: guardErr}
		}
	}()
	if g == nil {
		return nil
	}

	if err := g.outcomeRejection(toolName, buildCallKey(toolName, arguments)); err != nil {
		return err
	}
	if isStatusPollingTool(toolName) {
		return nil
	}

	if toolName == "bash" {
		cmd, dir := extractBashRequest(arguments)
		if cmd != "" && bashFailureKey(cmd, dir) == g.lastFailedKey && g.failureStreak >= g.maxConsecutiveFailures {
			return fmt.Errorf(RuntimeMessage("loop_failing_command", "loop detected: command '%s' has failed %d times in a row. Stop repeating this failing command; address the error or adjust arguments before retrying"), cmd, g.failureStreak)
		}
	}

	if toolName == "read" {
		if target := g.ExtractTargetFile(toolName, arguments); target != "" && target == g.lastTarget && g.targetStreak >= g.maxConsecutiveReads && readWindowSeen(target, arguments, g.readWindows) {
			return fmt.Errorf(RuntimeMessage("loop_repeated_target", "loop detected: target '%s' has already been inspected %d times in a row and its contents are already in context above. Inspect the existing output or proceed with your next action"), target, g.targetStreak)
		}
	}

	callKey := buildCallKey(toolName, arguments)
	if callKey == g.lastCallKey && g.callStreak >= g.maxConsecutiveCalls {
		return fmt.Errorf(RuntimeMessage("loop_identical_call", "loop detected: identical tool call repeated %d times in a row. Stop repeating the same call and choose a different action"), g.callStreak)
	}

	return nil
}

func (g *TurnExecutionGuard) RecordPostExecution(toolName, arguments string, output string, err error) {
	g.RecordObservedResult(toolName, arguments, output, err, nil)
}

func (g *TurnExecutionGuard) RecordObservedResult(toolName, arguments string, output string, err error, observed *evidence.Record) {
	if g == nil {
		return
	}

	callKey := buildCallKey(toolName, arguments)
	target := ""
	if toolName == "read" {
		target = g.ExtractTargetFile(toolName, arguments)
	}
	var cmd, dir string
	if toolName == "bash" {
		cmd, dir = extractBashRequest(arguments)
		if observed != nil && observed.WorkingDirectoryChanged {
			g.forgetCommandResults()
		}
	}

	revisionChanged := g.recoverFileOutcomes(toolName, observed, err)
	_, previouslyObserved := g.outcomes[callKey]
	sameOutcome := g.recordOutcome(toolName, callKey, arguments, output, err)
	unchanged := observed != nil && observed.Changed != nil && !*observed.Changed && err == nil
	outcome := g.outcomes[callKey]
	outcome.noProgress = unchanged
	g.outcomes[callKey] = outcome
	g.recordFileOutcome(callKey, observed, err)
	// Changing output is progress for inspections and status polling. Mutating
	// tools retain the existing identical-call guard even on successful no-ops.
	inspection := tool.PolicyForName(toolName, arguments).ReadOnly
	if inspection && (revisionChanged || previouslyObserved && !sameOutcome) && err == nil {
		for key, outcome := range g.outcomes {
			if outcome.failed && outcome.file == "" {
				delete(g.outcomes, key)
			}
		}
		g.pruneOutcomeOrder()
		g.failureStreak, g.lastFailedKey = 0, ""
	}
	if callKey == g.lastCallKey && (!inspection || sameOutcome) {
		g.callStreak++
	} else {
		g.callStreak = 1
		g.lastCallKey = callKey
	}

	newWindow := false
	if err == nil {
		newWindow = isReadWindowNew(toolName, target, arguments, g.readWindows)
	}
	if toolName == "read" && target != "" && target == g.lastTarget && !newWindow && sameOutcome {
		g.targetStreak++
	} else {
		g.targetStreak = 0
		g.lastTarget = target
		if toolName == "read" && target != "" {
			g.targetStreak = 1
		}
	}

	if toolName == "bash" && cmd != "" {
		failureKey := bashFailureKey(cmd, dir)
		if err != nil && failureKey == g.lastFailedKey {
			g.failureStreak++
		} else {
			g.failureStreak, g.lastFailedKey = 0, ""
			if err != nil {
				g.lastFailedKey, g.failureStreak = failureKey, 1
			}
		}
	}

	isModifying := err == nil && !unchanged && (toolName == "write" || toolName == "edit" || (toolName == "bash" && isBashModifying(cmd)))
	if isModifying {
		g.outcomes = make(map[string]observedOutcome)
		g.outcomeOrder = nil
		g.targetStreak = 0
		g.lastTarget = ""
		g.readWindows = make(map[string]map[string]bool)

		if err == nil {
			g.failureStreak, g.lastFailedKey = 0, ""
			if g.invalidator != nil && toolName == "bash" {
				g.invalidator.ForgetAll()
			}
		}
	}
}

func isReadWindowNew(toolName, target, arguments string, windows map[string]map[string]bool) bool {
	if toolName != "read" || windows == nil || target == "" {
		return false
	}

	windowKey, valid := readWindowKey(arguments)
	if !valid {
		return false
	}
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
	toolName = tool.NormalizeName(toolName)
	switch toolName {
	case "read", "write", "edit":
		return extractPathArg(arguments, "")
	case "list_directory", "ls":
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
	command, _ := extractBashRequest(arguments)
	return command
}

func extractBashRequest(arguments string) (string, string) {
	var raw string
	var args struct {
		Command   string `json:"command"`
		Cmd       string `json:"cmd"`
		Arguments string `json:"arguments"`
		Dir       string `json:"dir"`
		Cwd       string `json:"cwd"`
	}
	_ = tool.ParseToolArguments(arguments, &args, func(s string) { raw = s })
	command := strings.TrimSpace(tool.FirstNonEmpty(raw, args.Command, args.Cmd, args.Arguments))
	if command == "" {
		command = strings.TrimSpace(arguments)
	}
	directory := tool.FirstNonEmpty(args.Dir, args.Cwd)
	if directory != "" {
		directory = filepath.Clean(directory)
	}
	return command, directory
}

func bashFailureKey(command, directory string) string {
	return command + "\x00" + directory
}

func isBashModifying(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || bashtool.IsReadOnlyCommand(trimmed) {
		return false
	}
	if strings.Contains(trimmed, "sed -i") || strings.Contains(trimmed, "> ") || redirRe.MatchString(trimmed) {
		return true
	}
	for _, sub := range cmdSplitRe.Split(trimmed, -1) {
		sub = strings.TrimSpace(sub)
		if sub == "git stash" {
			return true
		}
		for _, p := range bashModifyPrefixes {
			if strings.HasPrefix(sub, p) {
				return true
			}
		}
	}
	return false
}

func readWindowKey(arguments string) (string, bool) {
	var args struct {
		Offset float64 `json:"offset"`
		Limit  float64 `json:"limit"`
	}
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return "", false
	}
	offset, count := max(1, int(args.Offset)), int(args.Limit)
	if count <= 0 {
		count = 2000
	}
	return fmt.Sprintf("%d:%d", offset, count), true
}

func readWindowSeen(target, arguments string, windows map[string]map[string]bool) bool {
	key, valid := readWindowKey(arguments)
	return valid && windows[target][key]
}

// RepetitionError identifies a rejected call that never executed. Its rejection
// must not be recorded as a fresh result or clear the evidence that blocked it.
type RepetitionError struct{ Cause error }

func (e *RepetitionError) Error() string { return e.Cause.Error() }
func (e *RepetitionError) Unwrap() error { return e.Cause }

func isRepetitionError(err error) bool {
	var repeated *RepetitionError
	return errors.As(err, &repeated)
}

func (g *TurnExecutionGuard) ForgetInspectionResults() {
	for key, outcome := range g.outcomes {
		if !outcome.failed && outcome.inspection {
			delete(g.outcomes, key)
		}
	}
	g.pruneOutcomeOrder()
	g.targetStreak = 0
	g.lastTarget = ""
	g.readWindows = make(map[string]map[string]bool)
	if _, retained := g.outcomes[g.lastCallKey]; !retained {
		g.callStreak = 0
		g.lastCallKey = ""
	}
}
