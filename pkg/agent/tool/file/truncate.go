package file

import (
	"strings"

	"loop/pkg/domain/limits"
	domaintool "loop/pkg/domain/tool"
)

// DefaultLimitTexts are the compiled fallbacks for the numeric-limit contract
// each tool description embeds, so the model knows its truncation budget before
// calling.
var DefaultLimitTexts = map[string]string{
	"read":              "Use the next offset in the continuation notice to read more. The page and its notice share the output budget.",
	"grep":              "Returns up to 100 matches (max 500), caps each matched line at 500 characters and the total output at 64KB. Notices report the next unread offset and a lower bound on matching lines. Use offset to continue.",
	"find_files":        "Returns up to 500 results (max 2000). Notices report the next unread offset and a lower bound on matching files. Use offset to continue.",
	"list_directory":    "Lists up to 150 entries (max 1000) at up to depth 3 (max 8). Notices state the exact next offset and the total entry count, so use offset to continue.",
	"bash":              "Long output is shortened. When a saved output path is shown, use read to retrieve it.",
	"read_param_offset": "Line number to start reading from (1-indexed). Optional",
	"find_param_offset": "Result number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
	"list_param_offset": "Entry number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
	"grep_param_offset": "Match number to start from (1-indexed). Optional; use the offset reported in the previous truncation notice to continue.",
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
	count := 0
	for index := range s {
		if count == maxRunes {
			return s[:index] + LineTruncatedMarker()
		}
		count++
	}
	return s
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

// PreviewWithNotice reserves space for the complete notice before taking text
// from either end. The result never exceeds the supplied byte budget.
func PreviewWithNotice(s, notice string, maxBytes int) string {
	if len(notice) >= maxBytes {
		return UTF8SafePrefix(notice, maxBytes)
	}
	head, tail := previewEnds(s, max(0, maxBytes-len(notice)-2))
	return joinTextPreview(head, notice, tail)
}

func previewEnds(s string, budget int) (string, string) {
	budget = min(budget, len(s))
	headBytes := (budget + 1) / 2
	return UTF8SafePrefix(s, headBytes), UTF8SafeSuffix(s, budget-headBytes)
}

func joinTextPreview(head, notice, tail string) string {
	if head != "" {
		notice = head + "\n" + notice
	}
	if tail != "" {
		notice += "\n" + tail
	}
	return notice
}

func LineTruncatedMarker() string {
	return domaintool.RuntimeMessage("read_line_truncated_marker", " ... [truncated]")
}

func ReadTruncationNotice(from, to, total, next int) string {
	return domaintool.RuntimeMessagef("read_truncation_notice",
		"\n\n[Showing lines %d to %d of %d total lines. Use read with offset=%d to continue.]", from, to, total, next)
}

func SearchLimitNotice(toolName string, shown, total, next int) string {
	toolName = domaintool.NormalizeName(toolName)
	switch toolName {
	case "find_files":
		return domaintool.RuntimeMessagef("find_result_limit_notice",
			"\n\n[Showing %d results of at least %d matches. Use find_files with offset=%d to continue.]", shown, total, next)
	case "list_directory":
		return domaintool.RuntimeMessagef("list_entry_limit_notice",
			"\n[Listing capped at %d entries of %d total. Use list_directory with offset=%d to continue.]", shown, total, next)
	default:
		return domaintool.RuntimeMessagef("search_result_limit_notice",
			"\n[Showing first %d results of %d total. Use %s with offset=%d to continue.]", shown, total, toolName, next)
	}
}

func GrepLimitNotice(shown, total, next int) string {
	return domaintool.RuntimeMessagef("grep_match_limit_notice",
		"\n[match limit reached: showing %d matches of at least %d matching lines. Use grep with offset=%d to continue.]", shown, total, next)
}

func GrepOutputNotice(kb, shown, total, next int) string {
	return domaintool.RuntimeMessagef("grep_output_limit_notice",
		"\n[output size limit reached: %d KB from %d matches of at least %d matching lines. Use grep with offset=%d to continue.]", kb, shown, total, next)
}

func OutputLimitNotice(toolName string, kb, shown, total, next int) string {
	return domaintool.RuntimeMessagef("output_limit_notice",
		"\n[output size limit reached: %d KB from %d results of %d total. Use %s with offset=%d to continue.]", kb, shown, total, toolName, next)
}

func SpillFullOutputNotice(locator string) string {
	if locator == "" {
		return ""
	}
	return domaintool.RuntimeMessagef("spill_full_output_notice", "\n[Read saved output: %s]", locator)
}
