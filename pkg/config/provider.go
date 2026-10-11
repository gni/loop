package config

import (
	"fmt"
	"os"
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

func (p ProviderConfig) ResolvedApiKey() string {
	key := strings.TrimSpace(p.ApiKey)
	if strings.HasPrefix(key, "$") {
		return os.Getenv(strings.TrimPrefix(key, "$"))
	}
	if strings.HasPrefix(key, "env:") {
		return os.Getenv(strings.TrimPrefix(key, "env:"))
	}
	return p.ApiKey
}

// AuthStatus reports whether the provider has credentials available before
// dispatch, validating provider authentication before resolving a model.
func (p ProviderConfig) AuthStatus() error {
	// A declared-but-unresolved credential is the failure mode; a provider with no
	// api_key at all stays allowed (keyless local endpoints, existing behaviour).
	raw := strings.TrimSpace(p.ApiKey)
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(p.ResolvedApiKey()) == "" {
		return fmt.Errorf("provider %q has no API key (set api_key or an env:$VAR reference)", p.Name)
	}
	return nil
}

// ResolveModelAlias prefers an alias over a dated version of the same model
// Exact aliases win; otherwise partial matches
// are split into aliases and dated versions, aliases sorted descending and the
// highest alias picked, falling back to the latest dated version.
func (c *Config) ResolveModelAlias(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || c == nil || len(c.Providers) == 0 {
		return pattern
	}
	var candidates []string
	for _, p := range c.Providers {
		if m := strings.TrimSpace(p.Model); m != "" && (m == pattern || strings.Contains(strings.ToLower(m), strings.ToLower(pattern))) {
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
		sortDesc(list)
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
			return false // dated version suffix, e.g. claude-sonnet-4-5-20250929
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

func sortDesc(list []string) {
	for i := 1; i < len(list); i++ {
		for j := 0; j < i; j++ {
			if list[j] < list[i] {
				list[j], list[i] = list[i], list[j]
			}
		}
	}
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
	if p.ApiKey == "" || (!strings.HasPrefix(p.ApiKey, "$") && !strings.HasPrefix(p.ApiKey, "env:")) || p.ResolvedApiKey() != c.ApiKey {
		p.ApiKey = c.ApiKey
	}
	p.Model = c.Model
	p.Timeout = c.Timeout
	c.Providers[c.ActiveProvider] = p
}
