// Package task describes task outcomes separately from execution lifecycle.
package task

import (
	"maps"
	"strings"

	"loop/pkg/domain/evidence"
)

const (
	Running      = "running"
	Completed    = "completed"
	Blocked      = "blocked"
	Unfinished   = "unfinished"
	Unverified   = "unverified"
	Failed       = "failed"
	Cancelled    = "cancelled"
	LimitReached = "limit_reached"
)

type Item struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type Observation struct {
	CallID   string           `json:"call_id"`
	Tool     string           `json:"tool"`
	Failed   bool             `json:"failed,omitempty"`
	Evidence *evidence.Record `json:"evidence,omitempty"`
}

// Status is an assessment, not proof of semantic correctness. Source identifies
// a model declaration or a conservative runtime assessment.
type State struct {
	ID              string            `json:"id"`
	Objective       string            `json:"objective"`
	ExpectedOutcome string            `json:"expected_outcome,omitempty"`
	Status          string            `json:"status"`
	Source          string            `json:"source"`
	Summary         string            `json:"summary,omitempty"`
	Remaining       []string          `json:"remaining,omitempty"`
	Plan            []Item            `json:"plan,omitempty"`
	Observations    []Observation     `json:"observations,omitempty"`
	Verification    Verification      `json:"verification"`
	Constraints     []string          `json:"constraints,omitempty"`
	Revisions       map[string]string `json:"revisions,omitempty"`
	Failures        []Observation     `json:"failures,omitempty"`
}

// Update is a model assessment; the original objective remains immutable.
type Update struct {
	Status           string    `json:"status"`
	Summary          string    `json:"summary"`
	ExpectedOutcome  string    `json:"expected_outcome"`
	Remaining        *[]string `json:"remaining"`
	VerificationNote string    `json:"verification_note,omitempty"`
}

// HasWork distinguishes executor activity or an explicit plan from casual chat.
// A generated assessment alone must not turn a greeting into a tracked task.
func HasWork(state *State) bool {
	return state != nil && (len(state.Observations) > 0 || len(state.Plan) > 0 || len(state.Remaining) > 0)
}

// CanResume includes interrupted requests that stopped before their first tool.
// Completed outcomes remain authoritative over older running snapshots.
func CanResume(state *State) bool {
	if state == nil || state.Status == Completed {
		return false
	}
	switch state.Status {
	case Failed, Cancelled, Blocked, LimitReached, Unfinished, Unverified:
		return true
	default:
		return HasWork(state)
	}
}

func Clone(input *State) *State {
	if input == nil {
		return nil
	}
	output := *input
	output.Remaining = append([]string(nil), input.Remaining...)
	output.Constraints = append([]string(nil), input.Constraints...)
	if input.Revisions != nil {
		output.Revisions = make(map[string]string, len(input.Revisions))
		maps.Copy(output.Revisions, input.Revisions)
	}
	output.Failures = append([]Observation(nil), input.Failures...)
	for i := range output.Failures {
		output.Failures[i].Evidence = evidence.Clone(input.Failures[i].Evidence)
	}
	output.Plan = append([]Item(nil), input.Plan...)
	output.Verification.Checks = evidence.CloneChecks(input.Verification.Checks)
	output.Observations = append([]Observation(nil), input.Observations...)
	for i := range output.Observations {
		output.Observations[i].Evidence = evidence.Clone(input.Observations[i].Evidence)
	}
	return &output
}

func (s *State) Pending() []string {
	if s == nil {
		return nil
	}
	var items []string
	seen := make(map[string]bool)
	for _, item := range s.Plan {
		if item.Status != "completed" {
			items = append(items, item.ID+": "+item.Description)
			seen[strings.TrimSpace(item.Description)] = true
			seen[item.ID] = true
			seen[item.ID+": "+item.Description] = true
		}
	}
	for _, item := range s.Remaining {
		key := strings.TrimSpace(item)
		if !seen[key] {
			items = append(items, item)
			seen[key] = true
		}
	}
	return items
}
