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
