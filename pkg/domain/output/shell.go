package output

import (
	"bytes"
	"io"
	"strings"
)

const (
	shellText uint8 = iota
	shellEscape
	shellCSI
	shellString
	shellDiscardCSI
	shellStringEscape
	shellIntermediate
)

// SafeShellOutputWriter passes text and SGR colors but suppresses terminal
// control sequences that would corrupt the prompt or redraw the display.
type SafeShellOutputWriter struct {
	w      io.Writer
	state  uint8
	csi    []byte
	colors bool
}

func NewSafeShellOutputWriter(w io.Writer) *SafeShellOutputWriter {
	return &SafeShellOutputWriter{w: w, colors: true}
}

// PlainShellText removes terminal controls and colors from captured text.
func PlainShellText(text string) string {
	if strings.IndexFunc(text, func(r rune) bool {
		return r == 0x7f || (r < 0x20 && r != '\n' && r != '\r' && r != '\t')
	}) < 0 {
		return text
	}
	var plain strings.Builder
	filter := &SafeShellOutputWriter{w: &plain}
	_, _ = filter.Write([]byte(text))
	return plain.String()
}

func (s *SafeShellOutputWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if s.state == shellText && !hasShellControls(data) {
		n, err := s.w.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return n, err
	}
	var out bytes.Buffer
	for _, b := range data {
		switch s.state {
		case shellText:
			switch {
			case b == 0x1b:
				s.state = shellEscape
			case b == '\n' || b == '\r' || b == '\t' || (b >= 0x20 && b != 0x7f):
				out.WriteByte(b)
			}
		case shellEscape:
			switch {
			case b == '[':
				s.state = shellCSI
				s.csi = s.csi[:0]
			case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X':
				s.state = shellString
			case b >= 0x20 && b <= 0x2f:
				s.state = shellIntermediate
			case b == 0x1b:
				// A new escape restarts the sequence.
			default:
				s.state = shellText
			}
		case shellIntermediate:
			if b == 0x1b {
				s.state = shellEscape
			} else if b < 0x20 || b > 0x2f {
				// Consume the final byte of sequences such as ESC ( B.
				s.state = shellText
			}
		case shellCSI:
			if b >= 0x40 && b <= 0x7e {
				if s.colors && b == 'm' && len(s.csi) < 64 {
					out.WriteString("\x1b[")
					out.Write(s.csi)
					out.WriteByte('m')
				}
				s.state = shellText
			} else if (b >= '0' && b <= '9') || b == ';' || b == ':' {
				if len(s.csi) < 64 {
					s.csi = append(s.csi, b)
				}
			} else {
				s.state = shellDiscardCSI
			}
		case shellString:
			if b == 0x07 {
				s.state = shellText
			} else if b == 0x1b {
				s.state = shellStringEscape
			}
		case shellDiscardCSI:
			if b >= 0x40 && b <= 0x7e {
				s.state = shellText
			}
		case shellStringEscape:
			if b == '\\' || b == 0x07 {
				s.state = shellText
			} else if b != 0x1b {
				s.state = shellString
			}
		}
	}
	if out.Len() > 0 {
		n, err := s.w.Write(out.Bytes())
		if err != nil {
			return 0, err
		}
		if n != out.Len() {
			return 0, io.ErrShortWrite
		}
	}
	return len(data), nil
}

func hasShellControls(data []byte) bool {
	for _, b := range data {
		if b == 0x7f || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') {
			return true
		}
	}
	return false
}
