package agent

import "loop/pkg/domain/limits"

func (a *Agent) GetLimits() limits.Limits {
	if a != nil && a.Config != nil {
		return limits.Resolve(a.Config.Limits)
	}
	return limits.Resolve(limits.Master)
}
