package limits

func For(owner any) Limits {
	if provider, ok := owner.(interface{ GetLimits() Limits }); ok {
		return Resolve(provider.GetLimits())
	}
	return Resolve(Master)
}
