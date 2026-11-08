package task

// Report is the model-facing progress view. Executor evidence and the original
// request remain in State instead of being echoed after every status update.
type Report struct {
	Status         string   `json:"status"`
	Summary        string   `json:"summary,omitempty"`
	Remaining      []string `json:"remaining,omitempty"`
	Plan           []Item   `json:"plan,omitempty"`
	StepsCompleted int      `json:"steps_completed,omitempty"`
	StepsTotal     int      `json:"steps_total,omitempty"`
	Pending        int      `json:"pending,omitempty"`
}

func ProgressReport(state *State, inspect bool) Report {
	if state == nil {
		return Report{}
	}
	r := Report{Status: state.Status, StepsTotal: len(state.Plan), Pending: len(state.Pending())}
	for _, item := range state.Plan {
		if item.Status == Completed {
			r.StepsCompleted++
		} else if inspect {
			r.Plan = append(r.Plan, item)
		}
	}
	if inspect {
		r.Summary = state.Summary
		r.Remaining = append([]string(nil), state.Remaining...)
	}
	return r
}
