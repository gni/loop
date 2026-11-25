package agent

import (
	"fmt"

	"loop/pkg/config"
)

// ConfigurationController coordinates changes with the session that owns execution.
type ConfigurationController interface {
	UpdateConfig(*config.Config) error
}

type MCPConfigurationController interface {
	UpdateConfigAndReconnect(*config.Config) error
}

func (a *Agent) UpdateConfigAndReconnect(next *config.Config) error {
	if a == nil {
		return fmt.Errorf("agent is unavailable")
	}
	if controller, ok := a.ConfigControl.(MCPConfigurationController); ok {
		return controller.UpdateConfigAndReconnect(next)
	}
	if a.ConfigControl != nil {
		return fmt.Errorf("configuration controller does not support MCP reconnect")
	}
	return a.ApplyConfigAndReconnect(next)
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
	Theme           string
	SyntaxTheme     string
}

// LiveSettingsSnapshot is a thread-safe value copy of live and presentation settings.
type LiveSettingsSnapshot struct {
	CollapseResults    bool
	ShowThinking       bool
	GuidedMode         bool
	AutoApprove        bool
	ParallelToolCalls  bool
	ShowTokens         bool
	ShowWorkspace      bool
	ShowSession        bool
	ShowReasoningBadge bool
	ShowCollapseBadge  bool
	ShowGitBranch      bool
	ShowStickyQuestion bool
	ShowAutoBadge      bool
	HoverTooltips      bool
	NativeScroll       bool
	MarkdownRender     bool
	BashSyntaxColor    bool
	ShowMicroseconds   bool
	StreamWrites       bool
	ReasoningEffort    string
	Model              string
	Theme              string
	SyntaxTheme        string
	AskUserMode        string
	MaxSubagentDepth   int
}

// LiveSettings returns a synchronized snapshot of live settings, safe for
// concurrent readers and nil agents or configurations.
func (a *Agent) LiveSettings() LiveSettingsSnapshot {
	if a == nil {
		return LiveSettingsSnapshot{StreamWrites: true, ShowWorkspace: true, ShowSession: true, ShowReasoningBadge: true, ShowCollapseBadge: true, ShowGitBranch: true, ShowStickyQuestion: true, ShowAutoBadge: true, HoverTooltips: true, NativeScroll: false, BashSyntaxColor: true}
	}
	a.LLMProviderMu.RLock()
	defer a.LLMProviderMu.RUnlock()
	if a.Config == nil {
		return LiveSettingsSnapshot{StreamWrites: true, ShowWorkspace: true, ShowSession: true, ShowReasoningBadge: true, ShowCollapseBadge: true, ShowGitBranch: true, ShowStickyQuestion: true, ShowAutoBadge: true, HoverTooltips: true, NativeScroll: false, BashSyntaxColor: true}
	}
	cfg := a.Config
	return LiveSettingsSnapshot{
		CollapseResults:    cfg.CollapseResults,
		ShowThinking:       cfg.ShowThinking,
		GuidedMode:         cfg.GuidedMode,
		AutoApprove:        cfg.AutoApprove,
		ParallelToolCalls:  cfg.ParallelToolCalls,
		ShowTokens:         cfg.ShowTokens,
		ShowWorkspace:      cfg.ShowWorkspace,
		ShowSession:        cfg.ShowSession,
		ShowReasoningBadge: cfg.ShowReasoningBadge,
		ShowCollapseBadge:  cfg.ShowCollapseBadge,
		ShowGitBranch:      cfg.ShowGitBranch,
		ShowStickyQuestion: cfg.ShowStickyQuestion,
		ShowAutoBadge:      cfg.ShowAutoBadge,
		HoverTooltips:      cfg.HoverTooltips,
		NativeScroll:       cfg.NativeScroll,
		MarkdownRender:     cfg.MarkdownRender,
		BashSyntaxColor:    cfg.BashSyntaxColor,
		ShowMicroseconds:   cfg.ShowMicroseconds,
		StreamWrites:       cfg.StreamWrites,
		ReasoningEffort:    cfg.ReasoningEffort,
		Model:              cfg.Model,
		Theme:              cfg.Theme,
		SyntaxTheme:        cfg.SyntaxTheme,
		AskUserMode:        cfg.AskUserMode,
		MaxSubagentDepth:   cfg.MaxSubagentDepth,
	}
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
	next := LiveSettings{
		CollapseResults: cfg.CollapseResults,
		ShowThinking:    cfg.ShowThinking,
		GuidedMode:      cfg.GuidedMode,
		ReasoningEffort: cfg.ReasoningEffort,
		Theme:           cfg.Theme,
		SyntaxTheme:     cfg.SyntaxTheme,
	}
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
	cfg.Theme, cfg.SyntaxTheme = next.Theme, next.SyntaxTheme
	if next.GuidedMode {
		cfg.AutoApprove, cfg.ParallelToolCalls = false, false
	}
	a.ensureGuided().SetEnabled(next.GuidedMode)
	a.markSystemPromptDirty()
	return nil
}
