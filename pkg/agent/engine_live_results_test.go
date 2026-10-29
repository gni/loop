package agent

import (
	"strings"
	"testing"
)

func TestVerifiedToolSummaryLine(t *testing.T) {
	cases := []struct {
		name, output, want string
	}{
		{"write", "⎿  /tmp/a.txt (42 bytes)\n", "⎿  /tmp/a.txt (42 bytes)"},
		{"edit", "8  - old\n8  + new\n⎿  /tmp/a.txt (+1 / -1 lines)\n", "⎿  /tmp/a.txt (+1 / -1 lines)"},
		{"error", "⎿  ✗ Error\npermission denied\n", ""},
		{"no-summary", "plain output\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifiedToolSummaryLine(tc.output)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "\n") {
				t.Fatalf("multiline summary: %q", got)
			}
		})
	}
}
