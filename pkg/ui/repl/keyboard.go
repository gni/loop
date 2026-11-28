package repl

import (
	"context"
	"strconv"
	"strings"

	"loop/pkg/domain/limits"
	"loop/pkg/ui/input"
)

// Normalize enhanced terminal key protocols before dispatch, including modal
// input and Ctrl+C. Bracketed paste stays literal rather than becoming keys.
func normalizeKeyboardInput(ctx context.Context, rawInput <-chan byte, historyInput func([]byte) []byte) <-chan byte {
	output := make(chan byte, 16384)
	go func() {
		defer close(output)
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		emit := func(data []byte) bool {
			for _, b := range data {
				select {
				case <-done:
					return false
				case output <- b:
				}
			}
			return true
		}
		send := func(data []byte) bool {
			if len(data) == 0 {
				return true
			}
			if historyInput != nil {
				data = historyInput(data)
			}
			return emit(data)
		}

		var pending []byte
		for {
			tok, ok := input.ReadToken(ctx, rawInput, &pending, limits.EscapeDisambiguation())
			if !ok {
				return
			}
			switch tok.Kind {
			case input.TokenIgnored:
				continue
			case input.TokenBytes, input.TokenEscape, input.TokenMouse:
				if !send(tok.Raw) {
					return
				}
			case input.TokenSS3:
				if !send(decodeSS3Sequence(tok.Raw)) {
					return
				}
			case input.TokenAltKey:
				// Alt+Enter inserts a newline via byte 30.
				if len(tok.Raw) == 2 && (tok.Raw[1] == '\r' || tok.Raw[1] == '\n') {
					if !send([]byte{30}) {
						return
					}
					continue
				}
				if !send(tok.Raw) {
					return
				}
			case input.TokenPaste:
				if tok.Text == "" {
					continue
				}
				framed := make([]byte, 0, len(input.BracketedPasteStart)+len(tok.Text)+len(input.BracketedPasteEnd))
				framed = append(framed, input.BracketedPasteStart...)
				framed = append(framed, tok.Text...)
				framed = append(framed, input.BracketedPasteEnd...)
				if !send(framed) {
					return
				}
			case input.TokenCSI:
				if !send(decodeKeyboardSequence(tok.Raw)) {
					return
				}
			}
		}
	}()
	return output
}

func decodeKeyboardSequence(seq []byte) []byte {
	text := string(seq)
	if !strings.HasPrefix(text, "\x1b[") || len(text) < 3 {
		return seq
	}

	final := text[len(text)-1]
	body := text[2 : len(text)-1]

	// Navigation keys may carry modifiers and event types even without CSI-u.
	// Normalize those reports before history filtering; releases (:3) are not edits.
	if strings.ContainsRune("ABCDHF~", rune(final)) && !strings.HasPrefix(text, "\x1b[27;") {
		fields := strings.Split(body, ";")
		modifier := 1
		valid := len(fields) <= 2
		if len(fields) == 2 {
			event := strings.Split(fields[1], ":")
			if len(event) > 1 && event[1] == "3" {
				return nil
			}
			var err error
			modifier, err = strconv.Atoi(event[0])
			valid = err == nil && modifier > 0
		}
		if valid {
			if final == '~' && (fields[0] == "5" || fields[0] == "6") {
				return []byte("\x1b[" + fields[0] + "~")
			}
			if final != '~' && (fields[0] == "" || fields[0] == "1") {
				modifier = 1 + (modifier-1)&7
				if modifier == 1 {
					return []byte{27, '[', final}
				}
				return []byte("\x1b[1;" + strconv.Itoa(modifier) + string(final))
			}
		}
	}

	// Handle REPL-specific CSI-u encodings (encoded LF and Ctrl+Space) before
	// delegating standard CSI-u, Kitty PUA keypad/functional keys, and release
	// suppression to input.NormalizeCSI.
	if final == 'u' {
		fields := strings.Split(body, ";")
		keys := strings.Split(fields[0], ":")
		code, err := strconv.Atoi(keys[0])
		if err != nil {
			return seq
		}
		mod := 1
		if len(fields) > 1 && fields[1] != "" {
			modifiers := strings.Split(fields[1], ":")
			if len(modifiers) > 1 && modifiers[1] == "3" {
				return nil
			}
			if modifiers[0] != "" {
				mod, err = strconv.Atoi(modifiers[0])
				if err != nil || mod < 1 {
					return seq
				}
			}
		}
		mask := mod - 1
		hasAlt := mask&2 != 0
		hasCtrl := mask&4 != 0

		switch code {
		case 10:
			return []byte{30}
		case 32:
			if hasCtrl && !hasAlt {
				return []byte{0}
			}
		}
	} else if final == '~' && strings.HasPrefix(text, "\x1b[27;") {
		fields := strings.Split(body, ";")
		if len(fields) == 3 {
			if c, err := strconv.Atoi(fields[2]); err == nil && c == 10 {
				return []byte{30}
			}
		}
	}

	if decoded, ok := input.NormalizeCSI(seq); ok {
		return mapREPLNormalizedKey(decoded)
	}

	return seq
}

func decodeSS3Sequence(seq []byte) []byte {
	if decoded, ok := input.NormalizeSS3(seq); ok {
		return mapREPLNormalizedKey(decoded)
	}
	return seq
}

// Convert modified Enter forms (\n from Shift/Ctrl+Enter, \x1b\r from Alt+Enter)
// into byte 30 to match Loop's prompt newline convention.
func mapREPLNormalizedKey(decoded []byte) []byte {
	if (len(decoded) == 1 && decoded[0] == '\n') || (len(decoded) == 2 && decoded[0] == '\x1b' && decoded[1] == '\r') {
		return []byte{30}
	}
	return decoded
}
