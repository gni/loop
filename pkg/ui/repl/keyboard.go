package repl

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Normalize enhanced terminal key protocols before dispatch, including modal
// input and Ctrl+C. Bracketed paste stays literal rather than becoming keys.
func normalizeKeyboardInput(input <-chan byte, historyInput func([]byte) []byte) <-chan byte {
	output := make(chan byte, 1000)
	go func() {
		defer close(output)
		pasted := false
		var tail string
		var searchPaste []byte
		consumePaste := false
		send := func(data []byte) {
			if historyInput != nil {
				data = historyInput(data)
			}
			for _, b := range data {
				output <- b
			}
		}
		for b := range input {
			if pasted {
				if consumePaste {
					searchPaste = append(searchPaste, b)
				} else {
					output <- b
				}
				tail += string([]byte{b})
				if len(tail) > 6 {
					tail = tail[len(tail)-6:]
				}
				if tail == "\x1b[201~" {
					if consumePaste && historyInput != nil {
						historyInput(append([]byte("\x1b[200~"), searchPaste...))
					}
					searchPaste = nil
					consumePaste = false
					pasted = false
					tail = ""
				}
				continue
			}
			if b != 27 {
				key := []byte{b}
				for b >= utf8.RuneSelf && !utf8.FullRune(key) && len(key) < utf8.UTFMax {
					next, ok := readNextRawByte(input, 50*time.Millisecond)
					if !ok {
						break
					}
					key = append(key, next)
				}
				send(key)
				continue
			}
			seq := []byte{b}
			next, ok := readNextRawByte(input, 50*time.Millisecond)
			if !ok {
				send(seq)
				continue
			}
			seq = append(seq, next)
			if next == '\r' || next == '\n' {
				send([]byte{30})
				continue
			} // Alt+Enter.
			if next == '[' || next == 'O' {
				for len(seq) < 64 {
					c, ok := readNextRawByte(input, 50*time.Millisecond)
					if !ok {
						break
					}
					seq = append(seq, c)
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			}
			if string(seq) == "\x1b[M" {
				for len(seq) < 6 {
					next, ok := readNextRawByte(input, 50*time.Millisecond)
					if !ok {
						break
					}
					seq = append(seq, next)
				}
			}
			if string(seq) == "\x1b[200~" {
				pasted = true
				if historyInput != nil {
					filtered := historyInput(seq)
					consumePaste = len(filtered) == 0
					for _, b := range filtered {
						output <- b
					}
					continue
				}
			}
			send(decodeKeyboardSequence(seq))
		}
	}()
	return output
}

func decodeKeyboardSequence(seq []byte) []byte {
	text := string(seq)
	if !strings.HasPrefix(text, "\x1b[") {
		return seq
	}
	// Navigation may carry modifiers and event types even without CSI-u.
	// Normalize those reports before history filtering; releases are not edits.
	if len(text) >= 3 {
		final := text[len(text)-1]
		if strings.ContainsRune("ABCDHF~", rune(final)) && !strings.HasPrefix(text, "\x1b[27;") {
			fields := strings.Split(text[2:len(text)-1], ";")
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
	}
	var code, mod int
	mod = 1
	if strings.HasSuffix(text, "u") {
		fields := strings.Split(text[2:len(text)-1], ";")
		keys := strings.Split(fields[0], ":")
		var err error
		code, err = strconv.Atoi(keys[0])
		if err != nil {
			return seq
		}
		if len(fields) > 1 {
			modifiers := strings.Split(fields[1], ":")
			mod, err = strconv.Atoi(modifiers[0])
			if err != nil {
				return seq
			}
			if len(modifiers) > 1 && modifiers[1] == "3" {
				return nil
			}
		}
		if len(keys) > 1 && mod > 0 && (mod-1)&1 != 0 {
			if shifted, e := strconv.Atoi(keys[1]); e == nil && shifted > 0 {
				code = shifted
			}
		}
	} else if strings.HasPrefix(text, "\x1b[27;") && strings.HasSuffix(text, "~") {
		fields := strings.Split(text[2:len(text)-1], ";")
		if len(fields) != 3 {
			return seq
		}
		var err error
		mod, err = strconv.Atoi(fields[1])
		if err != nil {
			return seq
		}
		code, err = strconv.Atoi(fields[2])
		if err != nil {
			return seq
		}
	} else {
		return seq
	}
	if mod < 1 {
		return seq
	}
	mask := mod - 1
	switch code {
	case 57344:
		code = 27
	case 57345:
		code = 13
	case 57346:
		code = 9
	case 57347:
		code = 127
	case 57349:
		return []byte("\x1b[3~")
	case 57350, 57351, 57352, 57353, 57356, 57357:
		final := map[int]byte{57350: 'D', 57351: 'C', 57352: 'A', 57353: 'B', 57356: 'H', 57357: 'F'}[code]
		if mask == 0 {
			return []byte{27, '[', final}
		}
		return []byte("\x1b[1;" + strconv.Itoa(mod) + string(final))
	}
	if code == 10 {
		return []byte{30} // An encoded line feed also inserts a newline.
	}
	if code == 13 {
		if mask&7 != 0 {
			return []byte{30} // Shift, Alt, or Ctrl+Enter inserts a newline.
		}
		return []byte{'\r'}
	}
	if mask&4 != 0 {
		upper := unicode.ToUpper(rune(code))
		if upper >= '@' && upper <= '_' {
			return []byte{byte(upper) & 31}
		}
		if code == 32 {
			return []byte{0}
		}
		return seq
	}
	if code == 27 || code == 9 || code == 127 {
		return []byte{byte(code)}
	}
	if code < 32 || code > utf8.MaxRune {
		return seq
	}
	r := rune(code)
	if mask&1 != 0 {
		r = unicode.ToUpper(r)
	}
	if mask&2 != 0 {
		return append([]byte{27}, []byte(string(r))...)
	}
	return []byte(string(r))
}
