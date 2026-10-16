package file

import (
	"strings"
	"unicode/utf8"

	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

// DefaultLimitTexts are the compiled fallbacks for the numeric-limit contract
// each tool description embeds, so the model knows its truncation budget before
// calling.
var DefaultLimitTexts = map[string]string{
	"read":              "Results are truncated to 2000 lines or 50KB (whichever is hit first), never mid-line; each line is capped at 1000 characters. Truncation notices state the exact next offset and the total line count, so use offset/limit to page through the rest until complete.",
	"grep":              "Returns up to 100 matches (max 500), caps each matched line at 500 characters and the total output at 64KB. Notices state the exact next offset and the total matching lines, so use offset to continue.",
	"find":              "Returns up to 500 results (max 2000). Notices state the exact next offset and the total result count, so use offset to continue.",
	"list":              "Lists up to 150 entries (max 1000) at up to depth 3 (max 8). Notices state the exact next offset and the total entry count, so use offset to continue.",
	"bash":              "Output is pruned to 25 head and 25 tail lines or 8192 bytes (whichever is hit first); the full unpruned output is spilled to .loop/spill/ and the notice states the exact next offset and the total line count.",
	"read_param_offset": "Line number to start reading from (1-indexed). Optional",
	"find_param_offset": "Result number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
	"list_param_offset": "Entry number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
	"grep_param_offset": "Match number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
}

// LimitText returns the adjustable numeric-limit contract for a tool, falling
// back to the compiled default when the catalog has no entry.
func LimitText(toolName string) string {
	return domaintool.RuntimeMessage("limit_text_"+toolName, DefaultLimitTexts[toolName])
}

// HeadTruncation is the outcome of applying the line limit and the byte limit to
// a slice of complete lines.
type HeadTruncation struct {
	Content    string
	ShownLines int
	TotalLines int
	Truncated  bool
	ByLines    bool
	ByBytes    bool
}

// UTF8SafePrefix returns the longest prefix of s that is at most maxBytes long
// and ends on a UTF-8 boundary.
func UTF8SafePrefix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// UTF8SafeSuffix returns the longest suffix of s that is at most maxBytes long
// and starts on a UTF-8 boundary.
func UTF8SafeSuffix(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := len(s) - maxBytes
	for n < len(s) && s[n]&0xC0 == 0x80 {
		n++
	}
	return s[n:]
}

// TruncateLine caps a single line at maxRunes characters.
func TruncateLine(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\r", "")
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + LineTruncatedMarker()
}

// TruncateHead applies the line and byte limits to complete lines, first hit
// wins, and never returns a partial line.
func TruncateHead(lines []string, maxLines, maxBytes int) HeadTruncation {
	if maxLines <= 0 {
		maxLines = limits.MaxOutputLines()
	}
	if maxBytes <= 0 {
		maxBytes = limits.MaxOutputBytes()
	}

	res := HeadTruncation{TotalLines: len(lines)}
	shown := 0
	bytesUsed := 0
	for _, line := range lines {
		if shown >= maxLines {
			res.ByLines = true
			break
		}
		add := len(line)
		if shown > 0 {
			add++
		}
		if bytesUsed+add > maxBytes {
			res.ByBytes = true
			break
		}
		bytesUsed += add
		shown++
	}

	res.ShownLines = shown
	res.Content = strings.Join(lines[:shown], "\n")
	res.Truncated = shown < len(lines)
	return res
}

// TruncateMiddle keeps a head half and a tail half and marks the gap with an
// explicit character count. Both halves are cut on UTF-8 boundaries.
func TruncateMiddle(s string, maxBytes int) (string, int) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, 0
	}
	half := maxBytes / 2
	if half < 1 {
		half = 1
	}
	head := UTF8SafePrefix(s, half)
	tail := UTF8SafeSuffix(s, maxBytes-half)
	omitted := RuneCount(s) - RuneCount(head) - RuneCount(tail)
	if omitted < 0 {
		omitted = 0
	}
	return head + MiddleTruncationMarker(omitted) + tail, omitted
}

// RuneCount counts Unicode code points in s.
func RuneCount(s string) int {
	return utf8.RuneCountInString(s)
}

func LineTruncatedMarker() string {
	return domaintool.RuntimeMessage("read_line_truncated_marker", " ... [truncated]")
}

func MiddleTruncationMarker(chars int) string {
	return domaintool.RuntimeMessagef("middle_truncation_marker", "\n\n[... %d characters truncated ...]", chars)
}

func ReadTruncationNotice(from, to, total, next int) string {
	return domaintool.RuntimeMessagef("read_truncation_notice",
		"\n\n[Showing lines %d to %d of %d total lines. Use read with offset=%d to continue.]", from, to, total, next)
}

func ReadMoreLinesNotice(more, total, next int) string {
	return domaintool.RuntimeMessagef("read_more_lines_notice",
		"\n[%d more lines in file (%d total lines). Use read with offset=%d to continue.]", more, total, next)
}

func ReadFirstLineOverBudgetNotice(maxBytes, lineNo int, path string) string {
	return domaintool.RuntimeMessagef("read_first_line_over_budget_notice",
		"\n[First line exceeds the %d byte budget. Run: sed -n '%d p' %s | head -c %d]", maxBytes, lineNo, path, maxBytes)
}

func SearchLimitNotice(toolName string, shown, total, next int) string {
	switch toolName {
	case "find":
		return domaintool.RuntimeMessagef("find_result_limit_notice",
			"\n\n[Showing first %d results of %d total. Use find with offset=%d to continue.]", shown, total, next)
	case "list":
		return domaintool.RuntimeMessagef("list_entry_limit_notice",
			"\n[Listing capped at %d entries of %d total. Use list with offset=%d to continue.]", shown, total, next)
	default:
		return domaintool.RuntimeMessagef("search_result_limit_notice",
			"\n[Showing first %d results of %d total. Use %s with offset=%d to continue.]", shown, total, toolName, next)
	}
}

func GrepLimitNotice(shown, total, next int) string {
	return domaintool.RuntimeMessagef("grep_match_limit_notice",
		"\n[match limit reached: showing first %d matches of %d total matching lines. Use grep with offset=%d to continue.]", shown, total, next)
}

func GrepOutputNotice(kb, shown, total, next int) string {
	return domaintool.RuntimeMessagef("grep_output_limit_notice",
		"\n[output size limit reached: %d KB from %d matches of %d total matching lines. Use grep with offset=%d to continue.]", kb, shown, total, next)
}

func OutputLimitNotice(toolName string, kb, shown, total, next int) string {
	return domaintool.RuntimeMessagef("output_limit_notice",
		"\n[output size limit reached: %d KB from %d results of %d total. Use %s with offset=%d to continue.]", kb, shown, total, toolName, next)
}

func SpillPruneNotice(omittedLines, omittedChars, nextOffset, totalLines int) string {
	return domaintool.RuntimeMessagef("spill_prune_notice",
		"\n\n[... %d lines (%d characters) omitted to protect context window ... Showing lines 1 to %d and the last lines of %d total lines. Use read with offset=%d on the spill file to continue.]",
		omittedLines, omittedChars, nextOffset-1, totalLines, nextOffset)
}

func SpillBytePruneNotice(omittedChars, shownChars, totalBytes, totalLines int) string {
	return domaintool.RuntimeMessagef("spill_byte_prune_notice",
		"\n\n[... %d characters omitted to protect context window ... Showing first %d characters of %d total bytes across %d total lines. Use read with offset=1 limit=1 on the spill file, or grep it, to inspect the rest.]",
		omittedChars, shownChars, totalBytes, totalLines)
}

func SpillFullOutputNotice(locator string) string {
	return domaintool.RuntimeMessagef("spill_full_output_notice", "[Full raw output preserved at: %s]", locator)
}

func SpillRetrievalHint() string {
	return domaintool.RuntimeMessage("spill_retrieval_hint",
		"[Hint: Call 'read' with offset/limit, or 'grep' on the spill file to inspect omitted sections]")
}