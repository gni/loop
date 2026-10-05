package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigProviders(t *testing.T) {
	// Create a temporary directory for config file
	tmpDir, err := os.MkdirTemp("", "maquis-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")

	// 1. Load default config and make sure it has empty Providers
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Providers == nil {
		t.Fatalf("expected Providers map to be initialized")
	}

	if len(cfg.Providers) != 1 {
		t.Errorf("expected Providers map to contain default provider, got %d items", len(cfg.Providers))
	}

	// 2. Add some providers
	cfg.Providers["openai"] = ProviderConfig{
		Name:     "openai",
		Endpoint: "http://openai-compatible.local",
		ApiKey:   "sk-test-key",
		Model:    "gpt-4o",
	}

	cfg.Providers["local"] = ProviderConfig{
		Name:     "local",
		Endpoint: "http://localhost:11434",
		ApiKey:   "",
		Model:    "llama3",
	}

	// 3. Save and reload to verify persistence
	err = SaveConfig(configPath, cfg)
	if err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	cfgReloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if len(cfgReloaded.Providers) != 3 {
		t.Errorf("expected 3 providers after reload, got %d", len(cfgReloaded.Providers))
	}

	pOpenAI, ok := cfgReloaded.Providers["openai"]
	if !ok || pOpenAI.Endpoint != "http://openai-compatible.local" || pOpenAI.Model != "gpt-4o" {
		t.Errorf("openai provider was not correctly reloaded: %+v", pOpenAI)
	}

	// 4. Test select/sync active provider
	cfgReloaded.ActiveProvider = "openai"
	cfgReloaded.SyncActiveProvider()

	if cfgReloaded.Endpoint != "http://openai-compatible.local" {
		t.Errorf("expected endpoint to be synced to http://openai-compatible.local, got %q", cfgReloaded.Endpoint)
	}
	if cfgReloaded.Model != "gpt-4o" {
		t.Errorf("expected model to be synced to gpt-4o, got %q", cfgReloaded.Model)
	}
	if cfgReloaded.ApiKey != "sk-test-key" {
		t.Errorf("expected API Key to be synced to sk-test-key, got %q", cfgReloaded.ApiKey)
	}

	// 5. Test update active provider
	cfgReloaded.Model = "gpt-4-turbo"
	cfgReloaded.UpdateActiveProvider()

	pOpenAIUpdated := cfgReloaded.Providers["openai"]
	if pOpenAIUpdated.Model != "gpt-4-turbo" {
		t.Errorf("expected active provider's model to be updated to gpt-4-turbo, got %q", pOpenAIUpdated.Model)
	}
}

func TestConfigProvidersUnmarshal(t *testing.T) {
	jsonStr := `{
		"endpoint": "http://localhost:8080",
		"model": "llama-3-instruct",
		"providers": {
			"openai": {
				"name": "openai",
				"endpoint": "http://openai-compatible.local",
				"api_key": "sk-12345",
				"model": "gpt-3.5-turbo"
			}
		},
		"active_provider": "openai"
	}`

	var cfg Config
	err := json.Unmarshal([]byte(jsonStr), &cfg)
	if err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if cfg.ActiveProvider != "openai" {
		t.Errorf("expected ActiveProvider to be openai, got %q", cfg.ActiveProvider)
	}

	cfg.SyncActiveProvider()

	if cfg.Endpoint != "http://openai-compatible.local" {
		t.Errorf("expected endpoint to be synced to provider's endpoint, got %q", cfg.Endpoint)
	}
	if cfg.ApiKey != "sk-12345" {
		t.Errorf("expected API Key to be synced to provider's API Key, got %q", cfg.ApiKey)
	}
	if cfg.Model != "gpt-3.5-turbo" {
		t.Errorf("expected model to be synced to provider's model, got %q", cfg.Model)
	}
}

func TestConfigTimeout(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "maquis-timeout-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.json")

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Default should be 120
	if cfg.Timeout != 120 {
		t.Errorf("expected default Timeout to be 120, got %d", cfg.Timeout)
	}

	// Change timeout and save
	cfg.Timeout = 45
	if err := SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	reloaded, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to reload config: %v", err)
	}

	if reloaded.Timeout != 45 {
		t.Errorf("expected reloaded Timeout to be 45, got %d", reloaded.Timeout)
	}

	// Test env override
	t.Setenv("MAQUIS_TIMEOUT", "75")
	envCfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config with env: %v", err)
	}
	if envCfg.Timeout != 75 {
		t.Errorf("expected MAQUIS_TIMEOUT override to be 75, got %d", envCfg.Timeout)
	}
}

func TestProviderTimeoutSync(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Timeout = 60
	cfg.Providers["fast"] = ProviderConfig{
		Name:     "fast",
		Endpoint: "http://fast.local",
		Timeout:  15,
	}
	cfg.Providers["slow"] = ProviderConfig{
		Name:     "slow",
		Endpoint: "http://slow.local",
		Timeout:  300,
	}
	cfg.Providers["default_timeout"] = ProviderConfig{
		Name:     "default_timeout",
		Endpoint: "http://def.local",
		Timeout:  0,
	}

	// Activate fast provider
	if err := cfg.ActivateProvider("fast"); err != nil {
		t.Fatalf("failed to activate fast provider: %v", err)
	}
	if cfg.Timeout != 15 {
		t.Errorf("expected Timeout to be 15 for fast provider, got %d", cfg.Timeout)
	}

	// Activate slow provider
	if err := cfg.ActivateProvider("slow"); err != nil {
		t.Fatalf("failed to activate slow provider: %v", err)
	}
	if cfg.Timeout != 300 {
		t.Errorf("expected Timeout to be 300 for slow provider, got %d", cfg.Timeout)
	}

	// Update active provider timeout
	cfg.Timeout = 250
	cfg.UpdateActiveProvider()
	if cfg.Providers["slow"].Timeout != 250 {
		t.Errorf("expected Providers['slow'].Timeout to be updated to 250, got %d", cfg.Providers["slow"].Timeout)
	}
}
