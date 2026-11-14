// Package evidence describes observed tool effects without inferring task completion.
package evidence

import "maps"

const (
	Succeeded   = "succeeded"
	Failed      = "failed"
	Started     = "started"
	NotExecuted = "not_executed"
	Unknown     = "unknown"
	Passed      = "passed"
	Skipped     = "skipped"
	Stale       = "stale"
	Unavailable = "unavailable"
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
	Kind      string `json:"kind"`
	Target    string `json:"target,omitempty"`
	Directory string `json:"directory,omitempty"`
	// WorkingDirectoryChanged describes persistent shell state, not file edits.
	WorkingDirectoryChanged bool              `json:"working_directory_changed,omitempty"`
	Revision                string            `json:"revision,omitempty"`
	MutationAt              int64             `json:"mutation_at,omitempty"`
	FileRevisions           map[string]string `json:"file_revisions,omitempty"`
	Digest                  string            `json:"digest,omitempty"`
	DurationMS              *int64            `json:"duration_ms,omitempty"`
	Outcome                 string            `json:"outcome"`
	Changed                 *bool             `json:"changed,omitempty"`
	ExitCode                *int              `json:"exit_code,omitempty"`
	TaskID                  string            `json:"task_id,omitempty"`
	Diagnostics             []Diagnostic      `json:"diagnostics,omitempty"`
	Checks                  []Check           `json:"checks,omitempty"`
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
	copy.Checks = CloneChecks(r.Checks)
	if r.FileRevisions != nil {
		copy.FileRevisions = make(map[string]string, len(r.FileRevisions))
		maps.Copy(copy.FileRevisions, r.FileRevisions)
	}
	if r.DurationMS != nil {
		value := *r.DurationMS
		copy.DurationMS = &value
	}
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
