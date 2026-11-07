package agent

import (
	"crypto/sha256"
	"fmt"

	"loop/pkg/agent/tool"
)

const maxGuardOutcomes = 128

type observedOutcome struct {
	digest     [32]byte
	repeats    int
	failed     bool
	inspection bool
	noProgress bool
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
