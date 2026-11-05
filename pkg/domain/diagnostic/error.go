// Package diagnostic describes actionable tool failures without parsing prose.
package diagnostic

import (
	"encoding/json"
	"errors"
)

type Code string

const (
	MissingPath      Code = "missing_path"
	ReadRequired     Code = "read_required"
	RevisionChanged  Code = "revision_changed"
	EditNotFound     Code = "edit_not_found"
	AmbiguousEdit    Code = "ambiguous_edit"
	PermissionDenied Code = "permission_denied"
)

type Error struct {
	Code   Code
	Target string
	Line   int
	Cause  error
}

func (e *Error) Error() string { return e.Cause.Error() }
func (e *Error) Unwrap() error { return e.Cause }

func WithTarget(err error, target string) error {
	var failure *Error
	if !errors.As(err, &failure) {
		return err
	}
	return &Error{Code: failure.Code, Target: target, Line: failure.Line, Cause: err}
}

// Recovery is persisted with a tool result. The suggested call is guidance,
// never authorization to execute it or bypass an approval/observation guard.
type Recovery struct {
	Code        Code            `json:"code"`
	Instruction string          `json:"instruction"`
	Tool        string          `json:"tool,omitempty"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
}
