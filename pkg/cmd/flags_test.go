package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"loop/pkg/config"
)

func TestFlagOverridesExplicitZeroAndEmpty(t *testing.T) {
	// Flag storage is package-level, so these cases must run serially.
	for _, tt := range []struct {
		flag  string
		seed  func(*config.Config)
		value func(*config.Config) any
		want  any
	}{
		{"steps", func(c *config.Config) { c.MaxReasoningSteps = 42 }, func(c *config.Config) any { return c.MaxReasoningSteps }, 0},
		{"context-limit", func(c *config.Config) { c.ContextWindowLimit = 8192 }, func(c *config.Config) any { return c.ContextWindowLimit }, 0},
		{"max-completion-tokens", func(c *config.Config) { c.MaxCompletionTokens = 1024 }, func(c *config.Config) any { return c.MaxCompletionTokens }, 0},
		{"endpoint", func(c *config.Config) { c.Endpoint = "http://localhost:8080/v1" }, func(c *config.Config) any { return c.Endpoint }, ""},
		{"model", func(c *config.Config) { c.Model = "saved-model" }, func(c *config.Config) any { return c.Model }, ""},
		{"reasoning", func(c *config.Config) { c.ReasoningEffort = "high" }, func(c *config.Config) any { return c.ReasoningEffort }, ""},
		{"debug-file", func(c *config.Config) { c.DebugLogFile = "saved.log" }, func(c *config.Config) any { return c.DebugLogFile }, ""},
	} {
		t.Run(tt.flag, func(t *testing.T) {
			cmd := &cobra.Command{Use: "loop"}
			initRootFlags(cmd)
			cfg := config.DefaultConfig()
			tt.seed(cfg)
			saved := tt.value(cfg)
			applyFlagOverrides(cmd, cfg)
			if got := tt.value(cfg); got != saved {
				t.Fatalf("omitted flag changed saved value: got %v, want %v", got, saved)
			}
			value := ""
			if tt.want == 0 {
				value = "0"
			}
			if err := cmd.ParseFlags([]string{"--" + tt.flag + "=" + value}); err != nil {
				t.Fatal(err)
			}
			applyFlagOverrides(cmd, cfg)
			if got := tt.value(cfg); got != tt.want {
				t.Fatalf("explicit flag ignored: got %v, want %v", got, tt.want)
			}
		})
	}
}
