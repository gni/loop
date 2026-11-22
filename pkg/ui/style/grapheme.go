package style

import (
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/width"
)

// DefaultTabWidth defines the canonical tab stop spacing used across TUI layout math.
const DefaultTabWidth = 8

func isZeroWidthRune(r rune) bool {
	if r == '\u200d' || unicode.IsControl(r) {
		return true
	}
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return true
	}
	if (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF) {
		return true
	}
	return false
}

func isRegionalIndicator(r rune) bool {
	return r >= 0x1F1E6 && r <= 0x1F1FF
}

func isSkinToneModifier(r rune) bool {
	return r >= 0x1F3FB && r <= 0x1F3FF
}

func isEmojiPresentationBase(r rune) bool {
	if r >= 0x1F300 && r <= 0x1FAFF {
		return true
	}
	if (r >= 0x2600 && r <= 0x27BF) || (r >= 0x231A && r <= 0x23FF) {
		return true
	}
	return false
}

// RuneWidth counts terminal columns for an isolated rune rather than bytes or code points.
func RuneWidth(r rune) int {
	if isZeroWidthRune(r) {
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// NextGrapheme decodes the next grapheme cluster from s starting at byte index 0,
// returning the cluster slice, its terminal column width, and its byte length.
// It handles combining marks, variation selectors (VS15/VS16), skin-tone modifiers,
// ZWJ emoji sequences, and regional-indicator flag pairs with zero heap allocations.
func NextGrapheme(s string) (cluster string, cellWidth int, size int) {
	if len(s) == 0 {
		return "", 0, 0
	}
	first, sz := utf8.DecodeRuneInString(s)
	end := sz
	if first == utf8.RuneError && sz == 1 {
		return s[:1], 1, 1
	}

	w := RuneWidth(first)

	// Regional indicator pairs form a single 2-cell flag glyph.
	if isRegionalIndicator(first) && end < len(s) {
		next, nsz := utf8.DecodeRuneInString(s[end:])
		if isRegionalIndicator(next) {
			end += nsz
			w = 2
		}
	}

	for end < len(s) {
		r, nsz := utf8.DecodeRuneInString(s[end:])
		switch {
		case r == '\u200d': // Zero-Width Joiner connects subsequent emoji in one cluster.
			end += nsz
			if end < len(s) {
				joined, jsz := utf8.DecodeRuneInString(s[end:])
				end += jsz
				if w < 2 && (RuneWidth(joined) == 2 || isEmojiPresentationBase(joined)) {
					w = 2
				}
			}
		case r == '\uFE0F': // Emoji presentation selector promotes symbols (e.g. warning sign) to 2 cells.
			end += nsz
			if w == 1 && (isEmojiPresentationBase(first) || first == '#' || first == '*' || (first >= '0' && first <= '9')) {
				w = 2
			}
		case r == '\uFE0E': // Text presentation selector keeps glyph at 1 cell.
			end += nsz
			if w > 1 && isEmojiPresentationBase(first) {
				w = 1
			}
		case isSkinToneModifier(r):
			end += nsz
			if w < 2 {
				w = 2
			}
		case r == '\u20E3': // Combining enclosing keycap.
			end += nsz
			if w < 2 {
				w = 2
			}
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || (r >= 0xFE00 && r <= 0xFE0D) || (r >= 0xE0100 && r <= 0xE01EF):
			end += nsz
		default:
			return s[:end], w, end
		}
	}

	return s[:end], w, end
}

// TabAdvance returns the number of terminal columns a tab character advances from col (0-based).
func TabAdvance(col, tabWidth int) int {
	if tabWidth <= 0 {
		tabWidth = DefaultTabWidth
	}
	return tabWidth - (col % tabWidth)
}
