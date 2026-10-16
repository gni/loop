package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

type ProviderConfig struct {
	Name     string            `json:"name,omitempty"`
	Endpoint string            `json:"endpoint"`
	ApiKey   string            `json:"api_key,omitempty"`
	Model    string            `json:"model,omitempty"`
	Timeout  int               `json:"timeout,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
}

type ProvidersFile struct {
	Active         string                    `json:"active,omitempty"`
	ActiveProvider string                    `json:"active_provider,omitempty"`
	Providers      map[string]ProviderConfig `json:"providers"`
}

func parseEnvVarKey(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	if strings.HasPrefix(trimmed, "$") {
		return strings.TrimPrefix(trimmed, "$"), true
	}
	if strings.HasPrefix(trimmed, "env:") {
		return strings.TrimPrefix(trimmed, "env:"), true
	}
	return "", false
}

func (p ProviderConfig) ResolvedApiKey() string {
	if envVar, ok := parseEnvVarKey(p.ApiKey); ok {
		return os.Getenv(envVar)
	}
	return p.ApiKey
}

func (p ProviderConfig) AuthStatus() error {
	raw := strings.TrimSpace(p.ApiKey)
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(p.ResolvedApiKey()) == "" {
		return fmt.Errorf("provider %q has no API key (set api_key or an env:$VAR reference)", p.Name)
	}
	return nil
}

func (c *Config) ResolveModelAlias(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || c == nil || len(c.Providers) == 0 {
		return pattern
	}
	var candidates []string
	lowerPattern := strings.ToLower(pattern)
	for _, p := range c.Providers {
		if m := strings.TrimSpace(p.Model); m != "" && (m == pattern || strings.Contains(strings.ToLower(m), lowerPattern)) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return pattern
	}
	var aliases, dated []string
	for _, m := range candidates {
		if isModelAlias(m) {
			aliases = append(aliases, m)
		} else {
			dated = append(dated, m)
		}
	}
	pick := func(list []string) string {
		sort.Slice(list, func(i, j int) bool { return list[i] > list[j] })
		return list[0]
	}
	if len(aliases) > 0 {
		return pick(aliases)
	}
	return pick(dated)
}

func isModelAlias(model string) bool {
	for _, seg := range strings.Split(model, "-") {
		if len(seg) == 8 && allDigits(seg) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (c *Config) ActivateProvider(name string) error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	if strings.TrimSpace(name) == "" {
		c.ActiveProvider = ""
		return nil
	}
	if c.Providers == nil {
		return fmt.Errorf("provider '%s' not found", name)
	}

	provider, ok := c.Providers[name]
	if !ok {
		return fmt.Errorf("provider '%s' not found", name)
	}
	if strings.TrimSpace(provider.Endpoint) == "" {
		return fmt.Errorf("provider '%s' has an empty endpoint", name)
	}

	c.ActiveProvider = name
	c.Endpoint = provider.Endpoint
	c.ApiKey = provider.ResolvedApiKey()
	if provider.Model != "" {
		c.Model = provider.Model
	}
	if provider.Timeout > 0 {
		c.Timeout = provider.Timeout
	}
	return nil
}

func (c *Config) SyncActiveProvider() {
	if c == nil || c.ActiveProvider == "" {
		return
	}
	_ = c.ActivateProvider(c.ActiveProvider)
}

func (c *Config) UpdateActiveProvider() {
	if c.ActiveProvider == "" || c.Providers == nil {
		return
	}
	p, ok := c.Providers[c.ActiveProvider]
	if !ok {
		return
	}
	p.Endpoint = c.Endpoint
	_, isEnv := parseEnvVarKey(p.ApiKey)
	if p.ApiKey == "" || !isEnv || p.ResolvedApiKey() != c.ApiKey {
		p.ApiKey = c.ApiKey
	}
	p.Model = c.Model
	p.Timeout = c.Timeout
	c.Providers[c.ActiveProvider] = p
}