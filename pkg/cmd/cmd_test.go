package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRootHelpCommand(t *testing.T) {
	rootCmd.SetArgs([]string{"--help"})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	err := rootCmd.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("root help execution failed: %v", err)
	}
	out := buf.String()
	for _, sub := range []string{"config", "mcp", "provider", "session"} {
		if !strings.Contains(out, sub) {
			t.Fatalf("expected root help to list %q, got: %s", sub, out)
		}
	}
}

func TestSubcommandHelpCommands(t *testing.T) {
	subcommands := []string{"config", "mcp", "provider", "session"}
	for _, sub := range subcommands {
		t.Run(sub, func(t *testing.T) {
			rootCmd.SetArgs([]string{sub, "--help"})
			var buf bytes.Buffer
			rootCmd.SetOut(&buf)
			err := rootCmd.ExecuteContext(context.Background())
			if err != nil {
				t.Fatalf("%s help command failed: %v", sub, err)
			}
			if buf.Len() == 0 {
				t.Fatalf("%s help output was empty", sub)
			}
		})
	}
}
