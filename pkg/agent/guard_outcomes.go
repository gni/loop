package agent

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"loop/pkg/agent/tool"
	"loop/pkg/domain/diagnostic"
	"loop/pkg/domain/evidence"
)

const maxGuardOutcomes = 128

type observedOutcome struct {
	digest     [32]byte
	repeats    int
	failed     bool
	inspection bool
	noProgress bool
	file       string
	revision   string
	failure    diagnostic.Code
}

// A successful current read resolves observation prerequisites for that file.
// Keep other diagnostics until their cause or the repository actually changes.
func (g *TurnExecutionGuard) recoverFileOutcomes(name string, observed *evidence.Record, err error) (revisionChanged bool) {
	if observed == nil || observed.Target == "" || !filepath.IsAbs(observed.Target) {
		return false
	}
	path := filepath.Clean(observed.Target)
	var failure *diagnostic.Error
	stale := errors.As(err, &failure) && failure.Code == diagnostic.RevisionChanged
	currentRead := name == "read" && err == nil && observed.Kind == "read" && observed.Revision != ""
	if !stale && !currentRead {
		return false
	}
	for key, outcome := range g.outcomes {
		if outcome.file == path && (strings.HasPrefix(key, "read:") && outcome.revision != "" && outcome.revision != observed.Revision || outcome.failed && outcome.failure == diagnostic.RevisionChanged) {
			revisionChanged = true
			break
		}
	}
	removed := false
	for key, outcome := range g.outcomes {
		if outcome.file != path {
			continue
		}
		if strings.HasPrefix(key, "read:") && (stale || outcome.revision != "" && outcome.revision != observed.Revision) {
			delete(g.outcomes, key)
			removed = true
			revisionChanged = true
		} else if currentRead && outcome.failed && (outcome.failure == diagnostic.ReadRequired || outcome.failure == diagnostic.RevisionChanged || revisionChanged) {
			delete(g.outcomes, key)
			removed = true
			revisionChanged = revisionChanged || outcome.failure == diagnostic.RevisionChanged
		}
	}
	if removed {
		g.finishFileOutcomeRemoval()
	}
	return revisionChanged
}

func (g *TurnExecutionGuard) forgetReadResult(key string) {
	delete(g.outcomes, key)
	g.finishFileOutcomeRemoval()
}

func (g *TurnExecutionGuard) finishFileOutcomeRemoval() {
	g.pruneOutcomeOrder()
	g.targetStreak, g.lastTarget = 0, ""
	if _, retained := g.outcomes[g.lastCallKey]; !retained {
		g.callStreak, g.lastCallKey = 0, ""
	}
}

func (g *TurnExecutionGuard) recordFileOutcome(key string, observed *evidence.Record, err error) {
	if observed == nil || observed.Target == "" || !filepath.IsAbs(observed.Target) || (observed.Kind != "read" && observed.Kind != "file") {
		return
	}
	outcome := g.outcomes[key]
	outcome.file, outcome.revision = filepath.Clean(observed.Target), observed.Revision
	var failure *diagnostic.Error
	if errors.As(err, &failure) {
		outcome.failure = failure.Code
	}
	g.outcomes[key] = outcome
}

func (g *TurnExecutionGuard) pruneOutcomeOrder() {
	order := g.outcomeOrder[:0]
	for _, key := range g.outcomeOrder {
		if _, retained := g.outcomes[key]; retained {
			order = append(order, key)
		}
	}
	clear(g.outcomeOrder[len(order):])
	g.outcomeOrder = order
}

func (g *TurnExecutionGuard) forgetCommandResults() {
	for key := range g.outcomes {
		if strings.HasPrefix(key, "bash:") {
			delete(g.outcomes, key)
		}
	}
	g.pruneOutcomeOrder()
	if strings.HasPrefix(g.lastCallKey, "bash:") {
		g.callStreak, g.lastCallKey = 0, ""
	}
	g.failureStreak, g.lastFailedKey = 0, ""
}

// recordOutcome detects unchanged failures and inspections even when the model
// alternates between calls. Only fingerprints and counters are retained.
func (g *TurnExecutionGuard) recordOutcome(name, key, arguments, output string, err error) (same bool) {
	if g.outcomes == nil {
		g.outcomes = make(map[string]observedOutcome)
	}
	previous, exists := g.outcomes[key]
	current := observedOutcome{digest: sha256.Sum256([]byte(output)), repeats: 1, failed: err != nil, inspection: tool.PolicyForName(name, arguments).ReadOnly}
	if err != nil {
		current.digest = sha256.Sum256([]byte(output + "\x00" + err.Error()))
	}
	same = exists && previous.digest == current.digest && previous.failed == current.failed
	if same {
		current.repeats = previous.repeats + 1
	}
	if !exists {
		if len(g.outcomeOrder) >= maxGuardOutcomes {
			delete(g.outcomes, g.outcomeOrder[0])
			g.outcomeOrder = g.outcomeOrder[1:]
		}
		g.outcomeOrder = append(g.outcomeOrder, key)
	}
	g.outcomes[key] = current
	return same
}

func isStatusPollingTool(name string) bool {
	name = tool.NormalizeName(name)
	return name == "get_task_status" || name == "inspect_subagent" || name == "list_subagents"
}

func (g *TurnExecutionGuard) outcomeRejection(name, key string) error {
	if isStatusPollingTool(name) {
		return nil
	}
	outcome := g.outcomes[key]
	if !outcome.failed && !outcome.inspection && !outcome.noProgress {
		return nil
	}
	if outcome.repeats < g.maxConsecutiveCalls {
		return nil
	}
	if outcome.failed {
		return fmt.Errorf("loop detected: %s returned the same failure %d times, including calls separated by other tools. Correct the reported cause before retrying", name, outcome.repeats)
	}
	if outcome.noProgress {
		return fmt.Errorf("loop detected: %s left the file unchanged %d times, including calls separated by other tools. Inspect the current content and revise the requested change", name, outcome.repeats)
	}
	return fmt.Errorf("loop detected: %s returned the same inspection result %d times without an observed change. Reuse that evidence or change the query/range", name, outcome.repeats)
}
