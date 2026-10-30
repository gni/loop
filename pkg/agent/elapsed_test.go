package agent

import (
	"bytes"
	"io"
	"regexp"
	"testing"
	"time"

	"loop/pkg/config"
	"loop/pkg/db"
	"loop/pkg/ui/style"
)

func TestFinalStatsFormatElapsedTimeAndMicroseconds(t *testing.T) {
	for _, precise := range []bool{false, true} {
		var output bytes.Buffer
		a := &Agent{Config: &config.Config{ShowTokens: true, ShowMicroseconds: precise}}
		a.renderFinalAgentTurn(&db.Message{CompletionTokens: 645}, NewFallbackStreamRenderer(io.Discard), nil, &output, &output, "", time.Now().Add(-(4*time.Minute + 24*time.Second)), style.GetTheme("plain"))
		pattern := `^645 out • \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \(4m 24s\)\n$`
		if precise {
			pattern = `^645 out • \d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \(4m 24\.\d{6}s\)\n$`
		}
		if shown := style.StripAnsi(output.String()); !regexp.MustCompile(pattern).MatchString(shown) {
			t.Fatalf("microseconds=%t: final stats = %q", precise, shown)
		}
	}
}
