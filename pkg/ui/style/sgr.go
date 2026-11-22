package style

import (
	"strconv"
	"strings"
)

type colorKind uint8

const (
	colorNone colorKind = iota
	colorIndexed
	colorRGB
)

type sgrColor struct {
	kind    colorKind
	index   uint8
	r, g, b uint8
}

// SGRState tracks terminal styling attributes in bounded, fixed-size memory.
// Unlike string concatenation (sgr += seq), it can never grow without bound across
// long streams or repeated partial SGR updates.
type SGRState struct {
	Bold      bool
	Dim       bool
	Italic    bool
	Underline bool
	Blink     bool
	Reverse   bool
	Hidden    bool
	Strike    bool
	FG        sgrColor
	BG        sgrColor
}

// IsZero reports whether all SGR attributes are at terminal defaults.
func (s SGRState) IsZero() bool {
	return s == (SGRState{})
}

// Reset clears all SGR attributes.
func (s *SGRState) Reset() {
	*s = SGRState{}
}

// Apply parses a complete SGR escape sequence ("\x1b[...m") and updates state in place.
func (s *SGRState) Apply(seq string) {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return
	}
	body := seq[2 : len(seq)-1]
	if body == "" {
		s.Reset()
		return
	}

	var params [16]int
	count := 0
	start := 0
	for i := 0; i <= len(body); i++ {
		if i == len(body) || body[i] == ';' || body[i] == ':' {
			if count < len(params) {
				if i == start {
					params[count] = 0
				} else {
					val, err := strconv.Atoi(body[start:i])
					if err != nil {
						val = 0
					}
					params[count] = val
				}
				count++
			}
			start = i + 1
		}
	}

	for i := 0; i < count; i++ {
		code := params[i]
		switch {
		case code == 0:
			s.Reset()
		case code == 1:
			s.Bold = true
		case code == 2:
			s.Dim = true
		case code == 3:
			s.Italic = true
		case code == 4:
			s.Underline = true
		case code == 5 || code == 6:
			s.Blink = true
		case code == 7:
			s.Reverse = true
		case code == 8:
			s.Hidden = true
		case code == 9:
			s.Strike = true
		case code == 21 || code == 22:
			s.Bold = false
			s.Dim = false
		case code == 23:
			s.Italic = false
		case code == 24:
			s.Underline = false
		case code == 25:
			s.Blink = false
		case code == 27:
			s.Reverse = false
		case code == 28:
			s.Hidden = false
		case code == 29:
			s.Strike = false
		case code >= 30 && code <= 37:
			s.FG = sgrColor{kind: colorIndexed, index: uint8(code - 30)}
		case code == 39:
			s.FG = sgrColor{}
		case code >= 90 && code <= 97:
			s.FG = sgrColor{kind: colorIndexed, index: uint8(code - 90 + 8)}
		case code >= 40 && code <= 47:
			s.BG = sgrColor{kind: colorIndexed, index: uint8(code - 40)}
		case code == 49:
			s.BG = sgrColor{}
		case code >= 100 && code <= 107:
			s.BG = sgrColor{kind: colorIndexed, index: uint8(code - 100 + 8)}
		case code == 38 || code == 48:
			isBG := code == 48
			if i+1 >= count {
				continue
			}
			mode := params[i+1]
			if mode == 5 && i+2 < count {
				c := sgrColor{kind: colorIndexed, index: uint8(clampByte(params[i+2]))}
				if isBG {
					s.BG = c
				} else {
					s.FG = c
				}
				i += 2
			} else if mode == 2 && i+4 < count {
				c := sgrColor{
					kind: colorRGB,
					r:    uint8(clampByte(params[i+2])),
					g:    uint8(clampByte(params[i+3])),
					b:    uint8(clampByte(params[i+4])),
				}
				if isBG {
					s.BG = c
				} else {
					s.FG = c
				}
				i += 4
			}
		}
	}
}

// Sequence renders a minimal canonical ANSI SGR sequence representing the current state.
// Returns "" when all attributes are at defaults.
func (s SGRState) Sequence() string {
	if s.IsZero() {
		return ""
	}
	var b strings.Builder
	b.Grow(32)
	b.WriteString("\x1b[")
	needSep := false
	writeCode := func(code int) {
		if needSep {
			b.WriteByte(';')
		}
		b.WriteString(strconv.Itoa(code))
		needSep = true
	}
	if s.Bold {
		writeCode(1)
	}
	if s.Dim {
		writeCode(2)
	}
	if s.Italic {
		writeCode(3)
	}
	if s.Underline {
		writeCode(4)
	}
	if s.Blink {
		writeCode(5)
	}
	if s.Reverse {
		writeCode(7)
	}
	if s.Hidden {
		writeCode(8)
	}
	if s.Strike {
		writeCode(9)
	}
	writeColor := func(c sgrColor, base, bright, ext int) {
		switch c.kind {
		case colorIndexed:
			if c.index < 8 {
				writeCode(base + int(c.index))
			} else if c.index < 16 {
				writeCode(bright + int(c.index-8))
			} else {
				writeCode(ext)
				writeCode(5)
				writeCode(int(c.index))
			}
		case colorRGB:
			writeCode(ext)
			writeCode(2)
			writeCode(int(c.r))
			writeCode(int(c.g))
			writeCode(int(c.b))
		}
	}
	writeColor(s.FG, 30, 90, 38)
	writeColor(s.BG, 40, 100, 48)
	b.WriteByte('m')
	return b.String()
}

func clampByte(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
