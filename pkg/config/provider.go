package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"loop/pkg/domain/model"
)

type OAuthConfig struct {
	AuthorizationURL string   `json:"authorization_url,omitempty"`
	TokenURL         string   `json:"token_url,omitempty"`
	RegistrationURL  string   `json:"registration_url,omitempty"`
	DeviceURL        string   `json:"device_url,omitempty"`
	ClientID         string   `json:"client_id,omitempty"`
	ClientSecret     string   `json:"client_secret,omitempty"`
	RedirectURI      string   `json:"redirect_uri,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
}

type ProviderConfig struct {
	ChatCapabilities ChatCapabilities  `json:"chat_capabilities,omitempty"`
	Models           []model.Model     `json:"models,omitempty"`
	OAuth            *OAuthConfig      `json:"oauth,omitempty"`
	API              string            `json:"api,omitempty"`
	Name             string            `json:"name,omitempty"`
	Endpoint         string            `json:"endpoint"`
	APIKey           string            `json:"api_key,omitempty"`
	Model            string            `json:"model,omitempty"`
	Timeout          int               `json:"timeout,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
}

type ProvidersFile struct {
	Active         string                    `json:"active,omitempty"`
	ActiveProvider string                    `json:"active_provider,omitempty"`
	Providers      map[string]ProviderConfig `json:"providers"`
}

func (p *ProviderConfig) UnmarshalJSON(data []byte) error {
	type plain ProviderConfig
	next := plain(*p)
	decoded := &next
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded == nil {
		return fmt.Errorf("provider configuration must contain a JSON object")
	}
	*p = ProviderConfig(*decoded)
	return nil
}

func parseEnvVarKey(key string) (string, bool) {
	trimmed := strings.TrimSpace(key)
	if strings.HasPrefix(trimmed, "$") {
		return strings.TrimPrefix(trimmed, "$"), true
	}
	if strings.HasPrefix(trimmed, "env:") {
		return strings.TrimPrefix(strings.TrimPrefix(trimmed, "env:"), "$"), true
	}
	return "", false
}

func (p ProviderConfig) ResolvedAPIKey() string {
	if envVar, ok := parseEnvVarKey(p.APIKey); ok {
		return os.Getenv(envVar)
	}
	return p.APIKey
}

func (p ProviderConfig) AuthStatus() error {
	raw := strings.TrimSpace(p.APIKey)
	if raw == "" {
		return nil
	}
	if strings.TrimSpace(p.ResolvedAPIKey()) == "" {
		return fmt.Errorf("provider %q has no API key (set api_key or an env:$VAR reference)", p.Name)
	}
	return nil
}

func (c *Config) ResolveModelAlias(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || c == nil || len(c.Providers) == 0 {
		return pattern
	}
	for _, p := range c.Providers {
		if p.Model == pattern {
			return pattern
		}
		for _, m := range p.Models {
			if m.ID == pattern {
				return pattern
			}
		}
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
	unique := make(map[string]bool)
	for _, candidate := range candidates {
		unique[candidate] = true
	}
	if len(unique) == 1 {
		return candidates[0]
	}
	return pattern
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

	if err := validateTimeoutValue("provider "+name+" timeout", int64(provider.Timeout)); err != nil {
		return err
	}
	c.ActiveProvider = name
	c.Endpoint = provider.Endpoint
	c.APIKey = provider.APIKey
	if provider.Model != "" {
		c.Model = provider.Model
	}
	if provider.Timeout > 0 {
		c.Timeout = provider.Timeout
	}
	return nil
}

func (c *Config) SyncActiveProvider() error {
	if c == nil || c.ActiveProvider == "" {
		return nil
	}
	return c.ActivateProvider(c.ActiveProvider)
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
	_, isEnv := parseEnvVarKey(p.APIKey)
	if p.APIKey == "" || !isEnv || p.ResolvedAPIKey() != c.APIKey {
		p.APIKey = c.APIKey
	}
	p.Model = c.Model
	p.Timeout = c.Timeout
	c.Providers[c.ActiveProvider] = p
}
