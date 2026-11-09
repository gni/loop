package config

import (
	"fmt"
	"maps"
	"reflect"

	"loop/pkg/domain/model"
)

func cloneOAuthConfig(oauth *OAuthConfig) *OAuthConfig {
	if oauth == nil {
		return nil
	}
	copied := *oauth
	if oauth.Scopes != nil {
		copied.Scopes = append([]string(nil), oauth.Scopes...)
	}
	return &copied
}

func CopyMCPServers(servers map[string]MCPServerConfig) map[string]MCPServerConfig {
	if servers == nil {
		return nil
	}
	out := make(map[string]MCPServerConfig, len(servers))
	for name, cfg := range servers {
		copied := cfg
		copied.OAuth = cloneOAuthConfig(cfg.OAuth)
		if cfg.Args != nil {
			copied.Args = append([]string(nil), cfg.Args...)
		}
		copied.Env = maps.Clone(cfg.Env)
		copied.Headers = maps.Clone(cfg.Headers)
		out[name] = copied
	}
	return out
}

func CopyProviders(providers map[string]ProviderConfig) map[string]ProviderConfig {
	if providers == nil {
		return nil
	}
	out := make(map[string]ProviderConfig, len(providers))
	for name, p := range providers {
		copied := p
		copied.OAuth = cloneOAuthConfig(p.OAuth)
		copied.Headers = maps.Clone(p.Headers)
		if p.Models != nil {
			copied.Models = make([]model.Model, len(p.Models))
			for i, m := range p.Models {
				cm := m
				if m.Input != nil {
					cm.Input = append([]string(nil), m.Input...)
				}
				if m.Reasoning != nil {
					cm.Reasoning = append([]string(nil), m.Reasoning...)
				}
				if m.Pricing != nil {
					pr := *m.Pricing
					if m.Pricing.CacheRead != nil {
						cr := *m.Pricing.CacheRead
						pr.CacheRead = &cr
					}
					if m.Pricing.CacheWrite != nil {
						cw := *m.Pricing.CacheWrite
						pr.CacheWrite = &cw
					}
					cm.Pricing = &pr
				}
				copied.Models[i] = cm
			}
		}
		out[name] = copied
	}
	return out
}

func MCPServersEqual(a, b map[string]MCPServerConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for name, cfgA := range a {
		cfgB, exists := b[name]
		if !exists {
			return false
		}
		if cfgA.Type != cfgB.Type || cfgA.Command != cfgB.Command || cfgA.URL != cfgB.URL || cfgA.Enabled != cfgB.Enabled || cfgA.AlwaysLoad != cfgB.AlwaysLoad || cfgA.Exposure != cfgB.Exposure || !reflect.DeepEqual(cfgA.OAuth, cfgB.OAuth) {
			return false
		}
		if len(cfgA.Args) != len(cfgB.Args) || !maps.Equal(cfgA.Env, cfgB.Env) || !maps.Equal(cfgA.Headers, cfgB.Headers) {
			return false
		}
		for i := range cfgA.Args {
			if cfgA.Args[i] != cfgB.Args[i] {
				return false
			}
		}
	}
	return true
}

func Clone(cfg *Config) (*Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration is unavailable")
	}
	cloned := *cfg
	if cfg.Nostop.IntervalSeconds != nil {
		sec := *cfg.Nostop.IntervalSeconds
		cloned.Nostop.IntervalSeconds = &sec
	}
	cloned.Sources = maps.Clone(cfg.Sources)
	if cloned.Sources == nil {
		cloned.Sources = make(map[string]string)
	}
	if cfg.PluginDirectories != nil {
		cloned.PluginDirectories = append([]string(nil), cfg.PluginDirectories...)
	}
	if cfg.ExtensionDirectories != nil {
		cloned.ExtensionDirectories = append([]string(nil), cfg.ExtensionDirectories...)
	}
	if cfg.PromptDirectories != nil {
		cloned.PromptDirectories = append([]string(nil), cfg.PromptDirectories...)
	}
	if cfg.SkillDirectories != nil {
		cloned.SkillDirectories = append([]string(nil), cfg.SkillDirectories...)
	}
	if cfg.DisabledTools != nil {
		cloned.DisabledTools = append([]string(nil), cfg.DisabledTools...)
	}
	if cfg.RepeatReminderThresholds != nil {
		cloned.RepeatReminderThresholds = append([]int(nil), cfg.RepeatReminderThresholds...)
	}
	cloned.MCPServers = CopyMCPServers(cfg.MCPServers)
	cloned.Providers = CopyProviders(cfg.Providers)
	cloned.Limits.ReasoningTokenBudgets = maps.Clone(cfg.Limits.ReasoningTokenBudgets)
	return &cloned, nil
}
