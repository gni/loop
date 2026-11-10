package limits

// ToolOutputBytes is the final text budget, including pagination notices.
func ToolOutputBytes(owner any) int {
	budget := DefaultRetainedOutputBytes
	if provider, ok := owner.(interface{ GetToolOutputByteLimit() int }); ok {
		if value := provider.GetToolOutputByteLimit(); value > 0 {
			budget = value
		}
	}
	return min(budget, For(owner).MaxOutputBytes)
}

func For(owner any) Limits {
	if provider, ok := owner.(interface{ GetLimits() Limits }); ok {
		return Resolve(provider.GetLimits())
	}
	return Current()
}
