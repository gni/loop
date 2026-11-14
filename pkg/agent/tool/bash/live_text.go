package bash

import (
	"bytes"
	"unicode/utf8"
)

// Live text has already passed through SafeShellOutputWriter, so its only
// escape sequences are complete SGR colors. Keep these and UTF-8 runes intact.
func liveTokenEnd(data []byte, start int) int {
	if data[start] == 0x1b && start+1 < len(data) && data[start+1] == '[' {
		if end := bytes.IndexByte(data[start+2:], 'm'); end >= 0 {
			return start + end + 3
		}
	}
	_, size := utf8.DecodeRune(data[start:])
	return start + size
}

func liveTextPrefix(data []byte, limit int) int {
	if len(data) <= limit {
		return len(data)
	}
	end := 0
	for end < len(data) {
		next := liveTokenEnd(data, end)
		if next > limit {
			break
		}
		end = next
	}
	return end
}

func liveTextSuffix(data []byte, limit int) []byte {
	start := len(data) - limit
	if start <= 0 {
		return data
	}
	if escape := bytes.LastIndexByte(data[:start], 0x1b); escape >= 0 {
		start = max(start, liveTokenEnd(data, escape))
	}
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return data[start:]
}

func liveTextEnd(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	start := len(data) - 1
	for start > 0 && len(data)-start < utf8.UTFMax && !utf8.RuneStart(data[start]) {
		start--
	}
	if !utf8.FullRune(data[start:]) {
		return start
	}
	return len(data)
}
