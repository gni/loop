package agent

import (
	"fmt"

	"loop/pkg/config"
)

// ConfigurationController coordinates changes with the session that owns execution.
type ConfigurationController interface {
	UpdateConfig(*config.Config) error
}

// Configuration returns a snapshot that callers can edit without changing live state.
func (a *Agent) Configuration() (*config.Config, error) {
	if a == nil {
		return nil, fmt.Errorf("agent is unavailable")
	}
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if a.Config == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	return config.Clone(a.Config)
}

// UpdateConfig uses the owning session's idle guard and settings checkpoint.
func (a *Agent) UpdateConfig(next *config.Config) error {
	if a == nil {
		return fmt.Errorf("agent is unavailable")
	}
	if a.ConfigControl != nil {
		return a.ConfigControl.UpdateConfig(next)
	}
	return a.ApplyConfig(next)
}

// LiveSettings are the controls that can change while a response is streaming.
// Execution settings continue through UpdateConfig and its session idle guard.
type LiveSettings struct {
	CollapseResults bool
	ShowThinking    bool
	GuidedMode      bool
	ReasoningEffort string
}

// UpdateLiveSettings owns synchronization and policy side effects for shortcuts.
// Callbacks only edit the supplied values and must not call back into the agent.
func (a *Agent) UpdateLiveSettings(change func(*LiveSettings)) error {
	if a == nil || change == nil {
		return fmt.Errorf("configuration update is unavailable")
	}
	a.LLMProviderMu.Lock()
	defer a.LLMProviderMu.Unlock()
	if a.Config == nil {
		return fmt.Errorf("configuration is unavailable")
	}
	if a.Lifetime != nil && a.Lifetime.Err() != nil {
		return a.Lifetime.Err()
	}
	cfg := a.Config
	next := LiveSettings{CollapseResults: cfg.CollapseResults, ShowThinking: cfg.ShowThinking,
		GuidedMode: cfg.GuidedMode, ReasoningEffort: cfg.ReasoningEffort}
	change(&next)
	if next.ReasoningEffort != cfg.ReasoningEffort {
		effort, err := config.ParseReasoningEffort(next.ReasoningEffort)
		if err != nil {
			return err
		}
		next.ReasoningEffort = effort
	}
	cfg.CollapseResults, cfg.ShowThinking = next.CollapseResults, next.ShowThinking
	cfg.GuidedMode, cfg.ReasoningEffort = next.GuidedMode, next.ReasoningEffort
	if next.GuidedMode {
		cfg.AutoApprove, cfg.ParallelToolCalls = false, false
	}
	a.ensureGuided().SetEnabled(next.GuidedMode)
	a.markSystemPromptDirty()
	return nil
}
