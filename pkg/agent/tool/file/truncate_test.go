package file

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"loop/pkg/domain/limits"
)

func TestUTF8SafePrefixNeverSplitsACharacter(t *testing.T) {
	s := "héllo wörld — ok" // accented characters are multi-byte
	for n := 1; n <= len(s); n++ {
		p := UTF8SafePrefix(s, n)
		if len(p) > n {
			t.Errorf("prefix %d exceeded budget", n)
		}
		if !utf8.ValidString(p) {
			t.Errorf("prefix at %d is not a valid UTF-8 string: %q", n, p)
		}
	}
	// A cut inside a multi-byte character must walk back to its start.
	if got := UTF8SafePrefix(s, 2); got != "h" {
		t.Errorf("prefix cut should land on a boundary, got %q", got)
	}
	if got := UTF8SafeSuffix(s, 6); !utf8.ValidString(got) {
		t.Errorf("suffix is not valid UTF-8: %q", got)
	}
}

func TestTruncateHeadFirstLimitWinsAndKeepsCompleteLines(t *testing.T) {
	lines := make([]string, 300)
	for i := range lines {
		lines[i] = strings.Repeat("x", 200)
	}

	// Byte budget (300*201 = 60300 bytes) is hit before the 400-line budget.
	cut := TruncateHead(lines, 400, 50*1024)
	if !cut.ByBytes || cut.ByLines {
		t.Errorf("expected byte limit to win, got byLines=%v byBytes=%v", cut.ByLines, cut.ByBytes)
	}
	if cut.ShownLines == 0 || cut.ShownLines >= len(lines) {
		t.Errorf("expected partial display, got %d", cut.ShownLines)
	}
	if strings.Contains(cut.Content, " ... [truncated]") {
		t.Errorf("head truncation must not mark partial lines")
	}
	if !cut.Truncated {
		t.Errorf("expected Truncated=true")
	}

	// Line budget wins when the byte budget is large.
	cut2 := TruncateHead(lines, 10, 1<<20)
	if !cut2.ByLines || cut2.ShownLines != 10 {
		t.Errorf("expected line limit to win with 10 lines, got %d byLines=%v", cut2.ShownLines, cut2.ByLines)
	}

	// No truncation when both budgets are large enough.
	cut3 := TruncateHead(lines[:3], 10, 1<<20)
	if cut3.Truncated {
		t.Errorf("expected no truncation")
	}
}

func TestTruncateLineUsesCatalogMarker(t *testing.T) {
	got := TruncateLine("日本語の長い行", 3)
	if got != "日本語"+LineTruncatedMarker() {
		t.Errorf("unexpected line truncation: %q", got)
	}
	if TruncateLine("short", 10) != "short" {
		t.Errorf("short line must be untouched")
	}
}

func TestTruncateMiddleReportsExactCharacterCount(t *testing.T) {
	s := strings.Repeat("é", 100) // 2 bytes each
	out, omitted := TruncateMiddle(s, 60)
	if omitted <= 0 {
		t.Fatalf("expected omitted characters")
	}
	// The budget covers the retained halves; the marker itself is metadata.
	if RuneCount(out)-RuneCount(MiddleTruncationMarker(omitted)) != 100-omitted {
		t.Errorf("marker count does not match retained characters: %d vs %d", omitted, RuneCount(out))
	}
	if !utf8.ValidString(out) {
		t.Errorf("middle truncation produced invalid UTF-8")
	}
	if !strings.Contains(out, MiddleTruncationMarker(omitted)) {
		t.Errorf("missing middle marker: %q", out)
	}
}

func TestNoticesStateExactNextOffsetAndTotal(t *testing.T) {
	n := ReadTruncationNotice(1, 10, 500, 11)
	if !strings.Contains(n, "of 500 total lines") || !strings.Contains(n, "offset=11") {
		t.Errorf("read notice is not actionable: %s", n)
	}
	if !strings.Contains(SearchLimitNotice("find", 5, 20, 6), "offset=6") {
		t.Errorf("find notice is not actionable")
	}
	if !strings.Contains(GrepLimitNotice(3, 9, 4), "offset=4") {
		t.Errorf("grep notice is not actionable")
	}
	if !strings.Contains(OutputLimitNotice("list", 50, 12, 40, 13), "offset=13") {
		t.Errorf("output notice is not actionable")
	}
	if !strings.Contains(ReadFirstLineOverBudgetNotice(limits.MaxOutputBytes(), 1, "x.go"), "sed -n '1 p' x.go") {
		t.Errorf("over-budget notice must give a runnable command")
	}
}

func TestReadPaginationContinuesAtTheReportedOffset(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &dummyAgentContext{root: tmpDir}

	var lines []string
	for i := 1; i <= 300; i++ {
		lines = append(lines, fmt.Sprintf("line %03d: %s", i, strings.Repeat("y", 120)))
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "pg.txt"), []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := NewReadTool()
	out, err := r.Execute(ctx, `{"path":"pg.txt","limit":10}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Both notice forms state the total line count and the exact next offset.
	if !strings.Contains(out, "300 total lines") || !strings.Contains(out, "offset=11") {
		t.Fatalf("read notice is not actionable: %s", out)
	}

	out2, err := r.Execute(ctx, `{"path":"pg.txt","limit":10,"offset":11}`)
	if err != nil {
		t.Fatalf("read offset: %v", err)
	}
	if !strings.Contains(out2, "line 011:") {
		t.Errorf("second page must start at the reported offset: %s", out2)
	}

	// A single line larger than the byte budget gets a runnable command, not a
	// pagination instruction that cannot reach it.
	if err := os.WriteFile(filepath.Join(tmpDir, "one.txt"), []byte(strings.Repeat("z", 60*1024)), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out3, err := r.Execute(ctx, `{"path":"one.txt","limit":5,"offset":1}`)
	if err != nil {
		t.Fatalf("read big line: %v", err)
	}
	if !strings.Contains(out3, "sed -n '1 p'") {
		t.Errorf("over-budget line must produce a runnable command: %s", out3)
	}

	// UTF-8 content survives a byte-budget cut.
	if err := os.WriteFile(filepath.Join(tmpDir, "u.txt"), []byte(strings.Join([]string{
		"match éééééééééé", strings.Repeat("é", 40000), strings.Repeat("é", 40000),
	}, "\n")), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	out4, err := r.Execute(ctx, `{"path":"u.txt","limit":2}`)
	if err != nil {
		t.Fatalf("read utf8: %v", err)
	}
	if !utf8.ValidString(out4) {
		t.Errorf("read produced invalid UTF-8: %q", out4)
	}
}
