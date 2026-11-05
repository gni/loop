package tool

// ExecutionPolicy describes effects independently of presentation and schema.
// Unknown tools require approval and ordered execution unless explicitly declared.
type ExecutionPolicy struct {
	ReadOnly         bool
	Parallel         bool
	RequiresApproval bool
	RequiresPlan     bool
	RetrySafe        bool
}

// PolicyProvider lets an integration declare an argument-dependent policy.
type PolicyProvider interface {
	ExecutionPolicy(arguments string) ExecutionPolicy
}
