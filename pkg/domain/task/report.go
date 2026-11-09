package task

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeProgressReport accepts the bracketed advisory suffix used by older
// sessions, but rejects additional JSON values or arbitrary trailing content.
func DecodeProgressReport(output string, destination any) error {
	decoder := json.NewDecoder(strings.NewReader(output))
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	suffix := strings.TrimSpace(output[decoder.InputOffset():])
	if suffix != "" {
		if !strings.HasPrefix(suffix, "[") || !strings.HasSuffix(suffix, "]") || json.Valid([]byte(suffix)) {
			return fmt.Errorf("unexpected content after progress report")
		}
		advisory := strings.TrimSpace(suffix[1 : len(suffix)-1])
		known := false
		for _, prefix := range []string{"Advisory Reminder:", "Advisory Loop Warning:", "Request progress is unchanged.", "This inspection has returned unchanged evidence", "The same failure has occurred"} {
			if strings.HasPrefix(advisory, prefix) {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unrecognized progress advisory")
		}
		var report map[string]json.RawMessage
		if err := json.Unmarshal(raw, &report); err != nil {
			return err
		}
		if report == nil {
			return fmt.Errorf("progress report must be an object")
		}
		if _, exists := report["advisory"]; !exists {
			report["advisory"], _ = json.Marshal(advisory)
		}
		var err error
		raw, err = json.Marshal(report)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(raw, destination)
}

// Report is the model-facing progress view. Executor evidence and the original
// request remain in State instead of being echoed after every status update.
type Report struct {
	Advisory       string   `json:"advisory,omitempty"`
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

// NeedsProgress leaves single tool lookups and casual chat out of progress UI.
func NeedsProgress(state *State) bool {
	return state != nil && (len(state.Observations) > 1 || len(state.Plan) > 0 || len(state.Remaining) > 0)
}

// ShowProgressNotice preserves meaningful milestones and problems, while
// routine reads, completion acknowledgments and repeated updates stay quiet.
func ShowProgressNotice(output, arguments string) bool {
	var report struct {
		Report
		Unchanged bool `json:"unchanged"`
	}
	if DecodeProgressReport(output, &report) != nil {
		return true
	}
	if report.Unchanged {
		return false
	}
	var args struct{ Action, Summary string }
	_ = json.Unmarshal([]byte(arguments), &args)
	if report.StepsTotal == 0 && len(report.Plan) > 0 {
		report.StepsTotal = len(report.Plan)
		for _, item := range report.Plan {
			if item.Status == Completed {
				report.StepsCompleted++
			}
		}
	}
	if args.Action == "get" {
		return false
	}
	switch report.Status {
	case Running:
		return report.StepsTotal > 1 && report.StepsCompleted > 0 && report.StepsCompleted < report.StepsTotal && (args.Summary != "" || report.Summary != "")
	case Completed:
		if report.Pending > 0 || len(report.Remaining) > 0 {
			return true
		}
		for _, item := range report.Plan {
			if item.Status != Completed {
				return true
			}
		}
		return false
	case Blocked, Unfinished, Unverified, Failed, Cancelled, LimitReached:
		return true
	default:
		return true
	}
}

// AddProgressAdvisory keeps the model-facing progress response valid JSON.
// Unknown fields are retained for old and future report versions.
func AddProgressAdvisory(output, advisory string) string {
	var report map[string]json.RawMessage
	if json.Unmarshal([]byte(output), &report) != nil || report == nil {
		return output + "\n\n[" + advisory + "]"
	}
	report["advisory"], _ = json.Marshal(advisory)
	encoded, err := json.Marshal(report)
	if err != nil {
		return output
	}
	return string(encoded)
}
