package input

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"loop/pkg/domain/limits"
)

// MaxPasteBytes bounds bracketed paste accumulation to prevent unbounded RAM growth.
const MaxPasteBytes = 1 << 20 // 1 MiB

// TokenKind identifies a single framed terminal input unit.
type TokenKind uint8

const (
	TokenBytes TokenKind = iota
	TokenEscape
	TokenAltKey
	TokenCSI
	TokenSS3
	TokenMouse
	TokenPaste
	TokenIgnored // Consumed terminal query responses (OSC/DCS/APC).
)

// Token represents one atomic input event framed from raw terminal bytes.
type Token struct {
	Kind TokenKind
	Raw  []byte
	Text string // Normalized payload for TokenPaste or decoded UTF-8 text.
}

// BracketedPasteStart and BracketedPasteEnd mark terminal bracketed paste framing.
const (
	BracketedPasteStart = "\x1b[200~"
	BracketedPasteEnd   = "\x1b[201~"
)

// NormalizePastedText normalizes line endings and strips raw control/ANSI sequences
// from pasted terminal text while preserving tabs and newlines.
func NormalizePastedText(raw []byte) string {
	cleaned := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		b := raw[i]
		if b == '\x1b' {
			i = skipEscapeBytes(raw, i)
			continue
		}
		if b == '\r' {
			if i+1 < len(raw) && raw[i+1] == '\n' {
				i++
			}
			cleaned = append(cleaned, '\n')
			i++
			continue
		}
		if b == '\n' || b == '\t' || b >= 0x20 {
			if b != 0x7f {
				cleaned = append(cleaned, b)
			}
		}
		i++
	}
	return string(cleaned)
}

func skipEscapeBytes(data []byte, start int) int {
	if start >= len(data) || data[start] != '\x1b' {
		return start
	}
	i := start + 1
	if i >= len(data) {
		return i
	}
	switch data[i] {
	case '[':
		i++
		for i < len(data) {
			final := data[i] >= 0x40 && data[i] <= 0x7e
			i++
			if final {
				return i
			}
		}
	case ']', 'P', 'X', '^', '_':
		i++
		for i < len(data) {
			if data[i] == '\a' {
				return i + 1
			}
			if data[i] == '\x1b' && i+1 < len(data) && data[i+1] == '\\' {
				return i + 2
			}
			i++
		}
	default:
		if data[i] >= 0x20 && data[i] <= 0x2f {
			for i < len(data) && data[i] >= 0x20 && data[i] <= 0x2f {
				i++
			}
			if i < len(data) {
				return i + 1
			}
			return i
		}
		return i + 1
	}
	return i
}

// ReadToken reads and frames exactly one complete terminal input token from rawChan,
// respecting ctx cancellation, UTF-8 continuation validation, complete CSI/SS3/mouse/
// OSC/DCS/APC boundaries, bounded bracketed paste, and a single ESC disambiguation timer.
func ReadToken(ctx context.Context, rawChan <-chan byte, pending *[]byte, escTimeout time.Duration) (Token, bool) {
	if escTimeout <= 0 {
		escTimeout = limits.EscapeDisambiguation()
	}

	nextByte := func() (byte, bool) {
		if pending != nil && len(*pending) > 0 {
			b := (*pending)[0]
			*pending = (*pending)[1:]
			return b, true
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return 0, false
			case b, ok := <-rawChan:
				return b, ok
			}
		}
		b, ok := <-rawChan
		return b, ok
	}

	nextByteWithTimeout := func(d time.Duration) (byte, bool, bool) {
		if pending != nil && len(*pending) > 0 {
			b := (*pending)[0]
			*pending = (*pending)[1:]
			return b, true, true
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		if ctx != nil {
			select {
			case <-ctx.Done():
				return 0, false, false
			case b, ok := <-rawChan:
				return b, ok, true
			case <-timer.C:
				return 0, false, false
			}
		}
		select {
		case b, ok := <-rawChan:
			return b, ok, true
		case <-timer.C:
			return 0, false, false
		}
	}

	unread := func(b byte) {
		if pending != nil {
			*pending = append([]byte{b}, *pending...)
		}
	}

	first, ok := nextByte()
	if !ok {
		return Token{}, false
	}

	// Non-ESC byte: validate multi-byte UTF-8 safely without swallowing ASCII on bad lead bytes.
	if first != '\x1b' {
		if first < 0x80 {
			return Token{Kind: TokenBytes, Raw: []byte{first}}, true
		}
		want := utf8SequenceLength(first)
		if want == 1 {
			return Token{Kind: TokenBytes, Raw: []byte{first}}, true
		}
		buf := make([]byte, 1, want)
		buf[0] = first
		for len(buf) < want {
			cont, got, inTime := nextByteWithTimeout(escTimeout)
			if !inTime || !got {
				break
			}
			if cont&0xC0 != 0x80 {
				// Invalid continuation byte: push it back so following ASCII is never lost.
				unread(cont)
				break
			}
			buf = append(buf, cont)
		}
		return Token{Kind: TokenBytes, Raw: buf}, true
	}

	// ESC sequence framing with a single disambiguation timeout.
	second, got, inTime := nextByteWithTimeout(escTimeout)
	if !inTime || !got {
		return Token{Kind: TokenEscape, Raw: []byte{'\x1b'}}, true
	}

	switch second {
	case '[':
		return readCSIToken(ctx, nextByteWithTimeout, escTimeout)
	case 'O':
		third, got3, inTime3 := nextByteWithTimeout(escTimeout)
		if !inTime3 || !got3 {
			return Token{Kind: TokenAltKey, Raw: []byte{'\x1b', 'O'}}, true
		}
		seq := []byte{'\x1b', 'O', third}
		// Support modified SS3 sequences such as \x1bO2M or \x1bO1;5D while
		// returning immediately on standard 3-byte SS3 sequences (\x1bOM, \x1bOA).
		b := third
		if !(b >= 0x40 && b <= 0x7e) && ((b >= '0' && b <= '9') || b == ';') {
			for len(seq) < 16 && !(b >= 0x40 && b <= 0x7e) {
				nb, ngot, ninTime := nextByteWithTimeout(escTimeout)
				if !ninTime || !ngot {
					break
				}
				if nb < 0x20 || nb >= 0x7f {
					unread(nb)
					break
				}
				seq = append(seq, nb)
				b = nb
			}
		}
		return Token{Kind: TokenSS3, Raw: seq}, true
	case ']', 'P', 'X', '^', '_':
		// Consume terminal string responses (OSC/DCS/SOS/PM/APC) cleanly so payloads never leak.
		consumeStringSequence(nextByteWithTimeout, second, escTimeout)
		return Token{Kind: TokenIgnored}, true
	default:
		// Alt+<key> or Alt+<UTF-8 rune>.
		seq := []byte{'\x1b', second}
		if second >= 0x80 {
			want := utf8SequenceLength(second)
			for len(seq)-1 < want {
				cont, g, it := nextByteWithTimeout(escTimeout)
				if !it || !g {
					break
				}
				if cont&0xC0 != 0x80 {
					unread(cont)
					break
				}
				seq = append(seq, cont)
			}
		}
		return Token{Kind: TokenAltKey, Raw: seq}, true
	}
}

func readCSIToken(ctx context.Context, nextTimeout func(time.Duration) (byte, bool, bool), timeout time.Duration) (Token, bool) {
	seq := make([]byte, 2, 16)
	seq[0] = '\x1b'
	seq[1] = '['

	// Read first parameter/introducer byte to detect X10 mouse (\x1b[M + 3 raw bytes).
	b, got, inTime := nextTimeout(timeout)
	if !inTime || !got {
		return Token{Kind: TokenAltKey, Raw: seq}, true
	}
	seq = append(seq, b)
	if b == 'M' {
		for range 3 {
			mb, mgot, minTime := nextTimeout(timeout)
			if !minTime || !mgot {
				break
			}
			seq = append(seq, mb)
		}
		return Token{Kind: TokenMouse, Raw: seq}, true
	}

	// Read remaining CSI parameter/intermediate bytes until final byte 0x40..0x7E.
	for len(seq) < 64 && !(b >= 0x40 && b <= 0x7e) {
		nb, ngot, ninTime := nextTimeout(timeout)
		if !ninTime || !ngot {
			break
		}
		seq = append(seq, nb)
		b = nb
	}

	// Bracketed paste framing (\x1b[200~ ... \x1b[201~) with size cap and cancellation.
	if bytes.Equal(seq, []byte(BracketedPasteStart)) {
		pasted, complete := readBracketedPasteBody(ctx, nextTimeout)
		if !complete && ctx != nil && ctx.Err() != nil {
			return Token{}, false
		}
		return Token{
			Kind: TokenPaste,
			Raw:  pasted,
			Text: NormalizePastedText(pasted),
		}, true
	}

	// SGR mouse (\x1b[<b;x;yM or m).
	if len(seq) >= 4 && seq[2] == '<' && (seq[len(seq)-1] == 'M' || seq[len(seq)-1] == 'm') {
		return Token{Kind: TokenMouse, Raw: seq}, true
	}

	return Token{Kind: TokenCSI, Raw: seq}, true
}

func readBracketedPasteBody(ctx context.Context, nextTimeout func(time.Duration) (byte, bool, bool)) ([]byte, bool) {
	endSeq := []byte(BracketedPasteEnd)
	var body []byte
	var tail []byte
	const idleLimit = 2 * time.Second
	for {
		if ctx != nil && ctx.Err() != nil {
			return body, false
		}
		b, got, inTime := nextTimeout(idleLimit)
		if !got {
			// Channel closed or idle timeout on truncated paste: never wedge forever.
			if !inTime && len(tail) > 0 {
				body = appendLimited(body, tail)
			}
			return body, false
		}
		tail = append(tail, b)
		if len(tail) > len(endSeq) {
			body = appendLimited(body, tail[:1])
			tail = tail[1:]
		}
		if len(tail) == len(endSeq) && bytes.Equal(tail, endSeq) {
			return body, true
		}
	}
}

func appendLimited(dst, src []byte) []byte {
	rem := MaxPasteBytes - len(dst)
	if rem <= 0 {
		return dst
	}
	if len(src) > rem {
		src = src[:rem]
	}
	return append(dst, src...)
}

func consumeStringSequence(nextTimeout func(time.Duration) (byte, bool, bool), introducer byte, timeout time.Duration) {
	const maxSeqBytes = 4096
	prevEsc := false
	for range maxSeqBytes {
		b, got, inTime := nextTimeout(timeout)
		if !inTime || !got {
			return
		}
		if introducer == ']' && b == '\a' {
			return
		}
		if prevEsc && b == '\\' {
			return
		}
		prevEsc = b == '\x1b'
	}
}

func utf8SequenceLength(lead byte) int {
	switch {
	case lead&0xE0 == 0xC0:
		return 2
	case lead&0xF0 == 0xE0:
		return 3
	case lead&0xF8 == 0xF0:
		return 4
	default:
		return 1
	}
}

func isKittyPUA(code int) bool {
	return code >= 0xE000 && code <= 0xF8FF
}

// NormalizeSS3 normalizes VT100/xterm application-keypad SS3 sequences
// (such as \x1bOM for Keypad Enter and \x1bOj..\x1bOy for keypad operators/digits)
// into standard bytes while leaving cursor/function SS3 sequences intact.
func NormalizeSS3(seq []byte) ([]byte, bool) {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != 'O' {
		return nil, false
	}
	final := seq[len(seq)-1]
	param := string(seq[2 : len(seq)-1])
	modifier := 1
	if param != "" {
		modStr := param
		if idx := strings.LastIndexByte(param, ';'); idx >= 0 {
			modStr = param[idx+1:]
		}
		m, err := strconv.Atoi(modStr)
		if err != nil || m < 1 {
			return nil, false
		}
		modifier = m
	}
	mask := modifier - 1
	hasShift := mask&1 != 0
	hasAlt := mask&2 != 0
	hasCtrl := mask&4 != 0

	if final == 'M' {
		if hasShift || hasCtrl {
			return []byte{'\n'}, true
		}
		if hasAlt {
			return []byte{'\x1b', '\r'}, true
		}
		return []byte{'\r'}, true
	}
	if hasCtrl || hasAlt {
		return nil, false
	}
	switch {
	case final >= 'p' && final <= 'y':
		return []byte{'0' + (final - 'p')}, true
	case final == 'X':
		return []byte{'='}, true
	case final == 'j':
		return []byte{'*'}, true
	case final == 'k':
		return []byte{'+'}, true
	case final == 'l':
		return []byte{','}, true
	case final == 'm':
		return []byte{'-'}, true
	case final == 'n':
		return []byte{'.'}, true
	case final == 'o':
		return []byte{'/'}, true
	}
	return nil, false
}

// NormalizeCSI normalizes Kitty CSI-u (\x1b[code;mod u) and xterm modifyOtherKeys
// (\x1b[27;mod;code~) into standard bytes while preserving Shift/Alt/Ctrl fidelity.
// Returns (decoded, true) when normalized (with an empty slice for ignored key
// release or unmapped PUA functional events), or (nil, false) to keep the original CSI intact.
func NormalizeCSI(seq []byte) ([]byte, bool) {
	if len(seq) < 4 || seq[0] != '\x1b' || seq[1] != '[' {
		return nil, false
	}
	final := seq[len(seq)-1]
	body := string(seq[2 : len(seq)-1])

	var keyCode, shiftedCode, modifier int
	switch final {
	case 'u':
		parts := strings.Split(body, ";")
		if len(parts) < 1 || len(parts) > 3 {
			return nil, false
		}
		keyParts := strings.Split(parts[0], ":")
		c, err := strconv.Atoi(keyParts[0])
		if err != nil {
			return nil, false
		}
		keyCode = c
		if len(keyParts) >= 2 && keyParts[1] != "" {
			if sc, scErr := strconv.Atoi(keyParts[1]); scErr == nil {
				shiftedCode = sc
			}
		}
		modifier = 1
		if len(parts) >= 2 && parts[1] != "" {
			modParts := strings.Split(parts[1], ":")
			if len(modParts) > 1 && modParts[1] == "3" {
				// Kitty key release event: consume without emitting input.
				return []byte{}, true
			}
			if modParts[0] != "" {
				m, err := strconv.Atoi(modParts[0])
				if err != nil {
					return nil, false
				}
				modifier = m
			}
		}
	case '~':
		parts := strings.Split(body, ";")
		if len(parts) != 3 || parts[0] != "27" {
			return nil, false
		}
		m, err1 := strconv.Atoi(parts[1])
		c, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			return nil, false
		}
		modifier, keyCode = m, c
	default:
		return nil, false
	}

	if modifier < 1 {
		return nil, false
	}
	mask := modifier - 1
	hasShift := mask&1 != 0
	hasAlt := mask&2 != 0
	hasCtrl := mask&4 != 0

	// Normalize Kitty PUA functional and keypad codepoints before control/text decoding.
	switch {
	case keyCode == 57344:
		keyCode = 27
	case keyCode == 57345 || keyCode == 57414: // Enter / KP_ENTER
		keyCode = 13
	case keyCode == 57346:
		keyCode = 9
	case keyCode == 57347:
		keyCode = 127
	case keyCode == 57349 || keyCode == 57426: // Delete / KP_DELETE
		return []byte("\x1b[3~"), true
	case keyCode == 57425: // KP_INSERT
		return []byte("\x1b[2~"), true
	case keyCode == 57421: // KP_PAGE_UP
		return []byte("\x1b[5~"), true
	case keyCode == 57422: // KP_PAGE_DOWN
		return []byte("\x1b[6~"), true
	case keyCode == 57350 || keyCode == 57417: // Left / KP_LEFT
		return encodeNavCSI('D', mask), true
	case keyCode == 57351 || keyCode == 57418: // Right / KP_RIGHT
		return encodeNavCSI('C', mask), true
	case keyCode == 57352 || keyCode == 57419: // Up / KP_UP
		return encodeNavCSI('A', mask), true
	case keyCode == 57353 || keyCode == 57420: // Down / KP_DOWN
		return encodeNavCSI('B', mask), true
	case keyCode == 57356 || keyCode == 57423: // Home / KP_HOME
		return encodeNavCSI('H', mask), true
	case keyCode == 57357 || keyCode == 57424: // End / KP_END
		return encodeNavCSI('F', mask), true
	case keyCode >= 57399 && keyCode <= 57408: // KP_0..KP_9
		keyCode = '0' + (keyCode - 57399)
	case keyCode == 57409: // KP_DECIMAL
		keyCode = '.'
	case keyCode == 57410: // KP_DIVIDE
		keyCode = '/'
	case keyCode == 57411: // KP_MULTIPLY
		keyCode = '*'
	case keyCode == 57412: // KP_SUBTRACT
		keyCode = '-'
	case keyCode == 57413: // KP_ADD
		keyCode = '+'
	case keyCode == 57415: // KP_EQUAL
		keyCode = '='
	case keyCode == 57416: // KP_SEPARATOR
		keyCode = ','
	case isKittyPUA(keyCode):
		// Never leak unmapped Kitty PUA functional/modifier keys (NumLock,
		// CapsLock, bare modifiers, F13-F35, media keys) as printable runes.
		return []byte{}, true
	}

	// Preserve Shift+Tab (\x1b[Z), Alt+Backspace (\x1b\x7f), and Ctrl+Backspace (\x08) accurately.
	switch keyCode {
	case 9: // Tab
		if hasShift && !hasCtrl && !hasAlt {
			return []byte("\x1b[Z"), true
		}
		if hasAlt && !hasCtrl {
			return []byte{'\x1b', '\t'}, true
		}
		if !hasCtrl && !hasAlt {
			return []byte{'\t'}, true
		}
		return nil, false
	case 13: // Enter / KP_ENTER
		// Shift+Enter or Ctrl+Enter inserts a newline (\n); plain Enter submits (\r).
		if hasShift || hasCtrl {
			return []byte{'\n'}, true
		}
		if hasAlt {
			return []byte{'\x1b', '\r'}, true
		}
		return []byte{'\r'}, true
	case 27: // Escape
		if !hasShift && !hasAlt && !hasCtrl {
			return []byte{'\x1b'}, true
		}
		return nil, false
	case 127, 8: // Backspace
		if hasCtrl && !hasAlt {
			return []byte{0x08}, true
		}
		if hasAlt && !hasCtrl {
			return []byte{'\x1b', 0x7f}, true
		}
		if !hasShift && !hasAlt && !hasCtrl {
			return []byte{0x7f}, true
		}
		return nil, false
	}

	if hasCtrl && !hasAlt {
		// Standard ASCII Ctrl mappings (Ctrl+A..Z, Ctrl+@, Ctrl+[.._).
		if keyCode >= 'a' && keyCode <= 'z' {
			return []byte{byte(keyCode - 'a' + 1)}, true
		}
		if keyCode >= 'A' && keyCode <= 'Z' {
			return []byte{byte(keyCode - 'A' + 1)}, true
		}
		if keyCode >= '@' && keyCode <= '_' {
			return []byte{byte(keyCode - '@')}, true
		}
		return nil, false
	}

	resolvedCode := keyCode
	if hasShift && shiftedCode >= 0x20 && shiftedCode <= utf8.MaxRune && !isKittyPUA(shiftedCode) && utf8.ValidRune(rune(shiftedCode)) {
		resolvedCode = shiftedCode
	}

	if hasAlt && !hasCtrl && resolvedCode >= 0x20 && resolvedCode <= utf8.MaxRune && utf8.ValidRune(rune(resolvedCode)) {
		r := rune(resolvedCode)
		if hasShift && shiftedCode == 0 && r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		var buf [1 + utf8.UTFMax]byte
		buf[0] = '\x1b'
		n := utf8.EncodeRune(buf[1:], r)
		return buf[:1+n], true
	}

	if !hasCtrl && !hasAlt && resolvedCode >= 0x20 && resolvedCode <= utf8.MaxRune && utf8.ValidRune(rune(resolvedCode)) {
		r := rune(resolvedCode)
		if hasShift && shiftedCode == 0 && r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		var buf [utf8.UTFMax]byte
		n := utf8.EncodeRune(buf[:], r)
		return buf[:n], true
	}

	return nil, false
}

func encodeNavCSI(nav byte, mask int) []byte {
	normMod := 1 + mask&7
	if normMod == 1 {
		return []byte{27, '[', nav}
	}
	return []byte("\x1b[1;" + strconv.Itoa(normMod) + string(nav))
}
