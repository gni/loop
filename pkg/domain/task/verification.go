package task

import (
	"time"

	"loop/pkg/domain/evidence"
)

type Verification struct {
	Stage     string           `json:"stage,omitempty"`
	Changed   bool             `json:"changed,omitempty"`
	Checks    []evidence.Check `json:"checks,omitempty"`
	Note      string           `json:"note,omitempty"`
	ChangedAt int64            `json:"changed_at,omitempty"`
}

func (v *Verification) Invalidate(target string) {
	v.noteMutation(0)
	for i := range v.Checks {
		if v.Checks[i].Kind != "syntax" || v.Checks[i].Target == target {
			v.Checks[i].Status = evidence.Stale
		}
	}
	v.Stage = "unverified"
}

func (v *Verification) noteMutation(at int64) {
	if at <= 0 {
		at = time.Now().UnixNano()
	}
	v.ChangedAt = max(v.ChangedAt, at)
}

func (v *Verification) Observe(record *evidence.Record) {
	if record == nil {
		return
	}
	if record.Changed != nil && *record.Changed {
		v.Changed = true
		v.noteMutation(record.MutationAt)
		for i := range v.Checks {
			_, childChanged := record.FileRevisions[v.Checks[i].Target]
			if (v.Checks[i].Kind != "syntax" || v.Checks[i].Target == record.Target || childChanged) && v.Checks[i].StartedAt < v.ChangedAt {
				v.Checks[i].Status = evidence.Stale
			}
		}
	}
	for _, check := range evidence.CloneChecks(record.Checks) {
		// Polling an old process must not revalidate source changed afterward.
		// Syntax checks describe the candidate revision supplied with a mutation.
		if check.Kind != "syntax" && v.ChangedAt > 0 && check.StartedAt < v.ChangedAt && (check.Status == evidence.Passed || check.Status == evidence.Failed) {
			check.Detail = "Source changed after this check started; previous result: " + check.Status
			check.Status = evidence.Stale
		}
		replaced := false
		for i := range v.Checks {
			if v.Checks[i].Kind == check.Kind && v.Checks[i].Target == check.Target && v.Checks[i].Command == check.Command {
				if v.Checks[i].StartedAt <= check.StartedAt {
					v.Checks[i] = check
				}
				replaced = true
				break
			}
		}
		if !replaced {
			v.Checks = append(v.Checks, check)
		}
	}
	if len(v.Checks) > 32 {
		v.Checks = append([]evidence.Check(nil), v.Checks[len(v.Checks)-32:]...)
	}
	v.Stage = "unverified"
	if v.Changed {
		v.Stage = "changed"
	}
	verified, checked, failed := false, false, false
	for _, check := range v.Checks {
		if check.Status == evidence.Failed || check.Status == evidence.Unknown || check.Status == evidence.Unavailable {
			failed = true
		}
		if check.Status == evidence.Passed {
			checked = true
			if check.Kind != "syntax" {
				verified = true
			}
		}
	}
	if checked {
		v.Stage = "checked"
	}
	if verified {
		v.Stage = "verified"
	}
	if failed {
		v.Stage = "unverified"
	}
}
