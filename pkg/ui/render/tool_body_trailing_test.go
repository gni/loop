package render

import (
	"fmt"
	"strings"
	"testing"
)

func TestTrimRenderedToolBody(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
	}{
		{"plain", "last line\n\n", "last line"},
		{"crlf", "last line\r\n\r\n", "last line"},
		{"reset after newline", "last line\n\n\x1b[0m", "last line\x1b[0m"},
		{"reset then newline", "last line\x1b[0m\n\n", "last line\x1b[0m"},
		{"multiple resets", "last line\n\x1b[0m\n\x1b[39m\n", "last line\x1b[0m\x1b[39m"},
		{"colored body", "\x1b[32m+ line\x1b[0m", "\x1b[32m+ line\x1b[0m"},
		{"internal paragraph", "First\n\nLast\n\x1b[0m", "First\n\nLast\x1b[0m"},
		{"non-SGR text", "last line\x1b[2K", "last line\x1b[2K"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimRenderedToolBody(tc.input); got != tc.want {
				t.Fatalf("trimRenderedToolBody(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTrimRenderedToolBodySummaryAdjacent(t *testing.T) {
	var output strings.Builder
	fmt.Fprintln(&output, trimRenderedToolBody("poem line\n\n\x1b[0m"))
	fmt.Fprintln(&output, "⎿  poems/file.txt (12 bytes)")
	if got, want := output.String(), "poem line\x1b[0m\n⎿  poems/file.txt (12 bytes)\n"; got != want {
		t.Fatalf("write replay layout = %q; want %q", got, want)
	}
}
