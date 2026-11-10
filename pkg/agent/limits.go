package agent

import "loop/pkg/domain/limits"

func (a *Agent) GetToolOutputByteLimit() int {
	if a != nil {
		a.LLMProviderMu.RLock()
		defer a.LLMProviderMu.RUnlock()
		if a.Config != nil && a.Config.MaxToolOutputBytes > 0 {
			return a.Config.MaxToolOutputBytes
		}
	}
	return limits.DefaultRetainedOutputBytes
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
