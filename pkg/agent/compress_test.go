package agent

import (
	"strings"
	"testing"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)


func TestCompactionTriggerIsReserveBased(t *testing.T) {
	limit := 128000

	// Below both the reserve line and the ratio ceiling: no trigger.
	if CompactionTrigger(int(0.70*float64(limit)), 0, limit) {
		t.Errorf("should not trigger below the ratio")
	}

	// Ratio still fires early for a configured low ratio.
	if !CompactionTrigger(int(0.85*float64(limit)), 0, limit) {
		t.Errorf("ratio ceiling should still trigger")
	}

	// The reserve protects the summarizer call itself plus the completion floor.
	// For a small window the reserve line is hit before the ratio ceiling, so the
	// reserve is what triggers: it fires once headroom drops below the reserve and
	// stays quiet while headroom is still available.
	reserve := limits.CompactionReserveTokens()
	window := reserve + 500
	// A window large enough to leave the reserve intact does not trigger until the
	// usage reaches limit-minus-reserve.
	big := 60000
	if CompactionTrigger(big-reserve-1, 0, big) {
		t.Errorf("trigger fired while the reserve was still intact")
	}
	if !CompactionTrigger(big-reserve, 0, big) {
		t.Errorf("reserve boundary should trigger")
	}
	if !CompactionTrigger(window, 0, window) {
		t.Errorf("reserve boundary should trigger")
	}

	// For a large window the ratio ceiling fires first, and completion tokens
	// count toward the decision.
	if !CompactionTrigger(int(0.79*float64(limit)), int(0.02*float64(limit)), limit) {
		t.Errorf("completion tokens should count toward the trigger")
	}

	// Zero/invalid window must not trigger.
	if CompactionTrigger(100, 0, 0) {
		t.Errorf("invalid window must not trigger")
	}

	// Keep-recent is token-budgeted: a huge recent tail cannot exceed the budget.
	msgs := make([]db.Message, 40)
	for i := range msgs {
		msgs[i] = db.Message{Role: "user", Content: strings.Repeat("x", 2000)}
	}
	kept := keepRecentMessageCount(msgs)
	if kept <= 2 || kept >= len(msgs) {
		t.Errorf("expected a budgeted subset, got %d of %d", kept, len(msgs))
	}
	if kept*2000/4 > limits.CompactionKeepRecentTokens()+2000 {
		t.Errorf("kept tail exceeded the keep-recent budget: %d messages", kept)
	}

	// A small history keeps everything (the minimum tail).
	if keepRecentMessageCount([]db.Message{{Role: "user", Content: "hi"}}) != 1 {
		t.Errorf("tiny history should keep what exists")
	}
}

func TestCompressionSummaryScaffoldIsCatalogDriven(t *testing.T) {
	got := domaintool.RuntimeMessage("compression_summary_scaffold", "[System: Below is a summary of the earlier conversation history:%s\n%s]")
	if got != "[System: Below is a summary of the earlier conversation history:%s\n%s]" {
		t.Errorf("catalog scaffold drifted from the inline fallback: %q", got)
	}
	// The compression prompt must enumerate what a summary has to preserve, so
	// file operations are not lost when history is folded.
	if !strings.Contains(domaintool.MasterSystemPrompts.CompressionPrompt, "File Modifications") {
		t.Errorf("compression prompt lost the file-operations requirement")
	}
}

func TestCompactionTriggerCustomRatio(t *testing.T) {
	limit := 1000000 // Large limit so reserve doesn't dominate

	// Default ratio is 0.80
	if CompactionTrigger(750000, 0, limit) {
		t.Errorf("should not trigger at 75%% with default ratio")
	}

	// Custom ratio 0.90
	if CompactionTrigger(850000, 0, limit, 0.90) {
		t.Errorf("should not trigger at 85%% when ratio is 0.90")
	}
	if !CompactionTrigger(910000, 0, limit, 0.90) {
		t.Errorf("should trigger at 91%% when ratio is 0.90")
	}

	// Agent method on 128k context window
	limit128k := 128000
	ag := &Agent{}
	// At default 0.95 on 128k, triggers above 95% (121600)
	if ag.CompactionTrigger(120000, 0, limit128k) {
		t.Errorf("default agent ratio 0.95 on 128k should not trigger at 93.75%%")
	}
	if !ag.CompactionTrigger(122000, 0, limit128k) {
		t.Errorf("default agent ratio 0.95 on 128k should trigger above 95%%")
	}

	// At 0.90 on 128k, 90% is 115200 tokens
	ag.Config = &config.Config{CompressionThreshold: 0.90}
	if ag.CompactionTrigger(114000, 0, limit128k) {
		t.Errorf("agent with 0.90 threshold on 128k should not trigger at 89%% (114000 tokens)")
	}
	if !ag.CompactionTrigger(116000, 0, limit128k) {
		t.Errorf("agent with 0.90 threshold on 128k should trigger at 90.6%% (116000 tokens)")
	}

	// At 0.95 on 128k, 95% is 121600 tokens
	ag.Config = &config.Config{CompressionThreshold: 0.95}
	if ag.CompactionTrigger(120000, 0, limit128k) {
		t.Errorf("agent with 0.95 threshold on 128k should not trigger at 93.75%% (120000 tokens)")
	}
	if !ag.CompactionTrigger(122000, 0, limit128k) {
		t.Errorf("agent with 0.95 threshold on 128k should trigger at 95.3%% (122000 tokens)")
	}
}
