package run

// FailureKind is the provider's classification, independent of diagnostic text.
type FailureKind uint8

const (
	PermanentFailure FailureKind = iota
	TransientFailure
	ContextOverflow
)

type InferenceFailure struct {
	Kind  FailureKind
	Cause error
}

func (e *InferenceFailure) Error() string { return e.Cause.Error() }
func (e *InferenceFailure) Unwrap() error { return e.Cause }
