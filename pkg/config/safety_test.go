package config

import "testing"

func TestGuidedModeNormalizesUnsafeFlags(t *testing.T) {
	cfg := DefaultConfig()
	cfg.GuidedMode = true
	cfg.AutoApprove = true
	cfg.ApprovalAlwaysAnswer = true
	cfg.ParallelToolCalls = true
	cfg.NormalizeSafety()
	if cfg.AutoApprove || cfg.ApprovalAlwaysAnswer || cfg.ParallelToolCalls {
		t.Fatalf("guided mode must disable unattended approval and parallel execution: %+v", cfg)
	}
}

func TestLocalDefaultsAreOptIn(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxSubagentDepth != 0 || cfg.EnableMCP || cfg.ParallelToolCalls || cfg.ReasoningEffort != "off" {
		t.Fatalf("unexpected advanced feature in default config: %+v", cfg)
	}
	if cfg.MaxReasoningSteps <= 0 || cfg.MaxReasoningSteps > 12 || !cfg.DisableRecap {
		t.Fatalf("expected bounded minimal agent loop defaults: %+v", cfg)
	}
}
