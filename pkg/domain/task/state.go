// Package task describes task outcomes separately from execution lifecycle.
package task

import "loop/pkg/domain/evidence"

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
	ID              string        `json:"id"`
	Objective       string        `json:"objective"`
	ExpectedOutcome string        `json:"expected_outcome,omitempty"`
	Status          string        `json:"status"`
	Source          string        `json:"source"`
	Summary         string        `json:"summary,omitempty"`
	Remaining       []string      `json:"remaining,omitempty"`
	Plan            []Item        `json:"plan,omitempty"`
	Observations    []Observation `json:"observations,omitempty"`
}

// Update is a model assessment; the original objective remains immutable.
type Update struct {
	Status          string    `json:"status"`
	Summary         string    `json:"summary"`
	ExpectedOutcome string    `json:"expected_outcome"`
	Remaining       *[]string `json:"remaining"`
}

// HasWork distinguishes executor activity or an explicit plan from casual chat.
// A generated assessment alone must not turn a greeting into a tracked task.
func HasWork(state *State) bool {
	return state != nil && (len(state.Observations) > 0 || len(state.Plan) > 0 || len(state.Remaining) > 0)
}

func Clone(input *State) *State {
	if input == nil {
		return nil
	}
	output := *input
	output.Remaining = append([]string(nil), input.Remaining...)
	output.Plan = append([]Item(nil), input.Plan...)
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
	items := append([]string(nil), s.Remaining...)
	for _, item := range s.Plan {
		if item.Status != "completed" {
			items = append(items, item.ID+": "+item.Description)
		}
	}
	return items
}
