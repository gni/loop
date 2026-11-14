package agent

import "loop/pkg/domain/limits"

func (a *Agent) GetToolOutputByteLimit() int {
	budget := limits.DefaultRetainedOutputBytes
	if a != nil {
		a.LLMProviderMu.RLock()
		if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
			budget = a.Config.MaxToolOutputBytes
		}
		a.LLMProviderMu.RUnlock()
		budget = min(budget, max(512, a.GetEffectiveContextLimit(0)/2))
	}
	return budget
}

func (a *Agent) GetLimits() limits.Limits {
	if a != nil {
		a.LLMProviderMu.RLock()
		defer a.LLMProviderMu.RUnlock()
		if a.Config != nil {
			return limits.Resolve(a.Config.Limits)
		}
	}
	return limits.Resolve(limits.Current())
}
