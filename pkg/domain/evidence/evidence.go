// Package evidence describes observed tool effects without inferring task completion.
package evidence

const (
	Succeeded   = "succeeded"
	Failed      = "failed"
	Started     = "started"
	NotExecuted = "not_executed"
	Unknown     = "unknown"
)

type Diagnostic struct {
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

// Record contains facts reported by the executor. Nil Changed and ExitCode
// mean unobserved, rather than unchanged or a successful command.
type Record struct {
	Kind        string       `json:"kind"`
	Target      string       `json:"target,omitempty"`
	Outcome     string       `json:"outcome"`
	Changed     *bool        `json:"changed,omitempty"`
	ExitCode    *int         `json:"exit_code,omitempty"`
	TaskID      string       `json:"task_id,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (r *Record) HasErrors() bool {
	if r == nil {
		return false
	}
	if r.Outcome == Failed {
		return true
	}
	for _, d := range r.Diagnostics {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func Clone(r *Record) *Record {
	if r == nil {
		return nil
	}
	copy := *r
	copy.Diagnostics = append([]Diagnostic(nil), r.Diagnostics...)
	if r.Changed != nil {
		v := *r.Changed
		copy.Changed = &v
	}
	if r.ExitCode != nil {
		v := *r.ExitCode
		copy.ExitCode = &v
	}
	return &copy
}
