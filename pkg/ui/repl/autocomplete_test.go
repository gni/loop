package repl

import (
	"testing"

	"loop/pkg/agent"
	"loop/pkg/config"
)

func TestProviderAutocomplete(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"brain":  {Endpoint: "https://brain.example"},
			"openai": {Endpoint: "http://openai-compatible.local"},
		},
	}
	a := &agent.Agent{Config: cfg}

	// 1. Completing "/p br"
	newLine, newPos, ok := autoCompleteCallback("/p br", 5, '\t', a)
	if !ok || newLine != "/p brain" {
		t.Fatalf("expected /p brain, got ok=%v, newLine=%q, newPos=%d", ok, newLine, newPos)
	}

	// 2. Completing "/provider op"
	newLine, newPos, ok = autoCompleteCallback("/provider op", 12, '\t', a)
	if !ok || newLine != "/provider openai" {
		t.Fatalf("expected /provider openai, got ok=%v, newLine=%q, newPos=%d", ok, newLine, newPos)
	}
}
